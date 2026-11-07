package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/denyszorinets/damson/internal/engine"
	"github.com/denyszorinets/damson/internal/expr"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// hookPrefix is the path of the webhook endpoint. The token is the rest, and a
// credential: request logs show "/hooks/[redacted]" (see logPath).
const hookPrefix = "/hooks/"

const (
	maxHookBody = 1 << 20
	// hookTokenLen is the length of base64url of 32 bytes.
	hookTokenLen = 43
)

// credentialHeaders never reach an options mapping.
var credentialHeaders = map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true, "x-gitlab-token": true, "x-api-key": true}

// hookHandler serves POST /hooks/{token} (ARCHITECTURE 12.3).
type hookHandler struct {
	d     deps
	env   *expr.Env
	limit *rateLimiter
}

func newHookHandler(d deps, env *expr.Env) *hookHandler {
	return &hookHandler{d: d, env: env, limit: newRateLimiter(defaultHookRate, defaultHookBurst)}
}

func validHookToken(t string) bool {
	if len(t) != hookTokenLen {
		return false
	}
	for _, c := range t {
		isLetter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !isLetter && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func hookNotFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, Problem{Type: typeNotFound, Title: "Not Found", Status: http.StatusNotFound, Detail: "no such webhook"})
}

func (h *hookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, hookPrefix)
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	if !validHookToken(token) {
		hookNotFound(w, r)
		return
	}
	hash := tokens.Hash(token)
	if ok, retry := h.limit.allow(hash, h.d.now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeProblem(w, r, Problem{Type: typeRateLimited, Title: "Too Many Requests", Status: http.StatusTooManyRequests, Detail: "this webhook is called too often"})
		return
	}
	if err := h.d.ready(); err != nil {
		writeProblem(w, r, problemFor(err))
		return
	}
	hook, err := h.d.ds.FindWebhook(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) {
		hookNotFound(w, r) // the same answer for an unknown and a revoked token
		return
	}
	if err != nil {
		writeProblem(w, r, problemFor(err))
		return
	}
	runID, err := h.start(w, r, hook)
	if err != nil {
		p := problemFor(err)
		logFailure(loggerFrom(r), p, err)
		writeProblem(w, r, p)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
}

// start builds the options, validates them and creates the run.
func (h *hookHandler) start(w http.ResponseWriter, r *http.Request, hook store.Webhook) (string, error) {
	ctx := r.Context()
	opts := map[string]any{}
	if len(hook.Options) > 0 {
		if err := json.Unmarshal(hook.Options, &opts); err != nil {
			return "", err
		}
	}
	if hook.OptionsMapping != "" {
		req, err := readHookRequest(w, r)
		if err != nil {
			return "", err
		}
		mapped, err := h.mapOptions(hook.OptionsMapping, req)
		if err != nil {
			loggerFrom(r).Info("webhook mapping failed", "webhook_id", hook.ID, "reason", mappingReason(err))
			return "", err
		}
		// The request may not choose which secret a run uses: options that
		// hold secret references come from the static options only.
		_, _, spec, err := h.d.loadSpec(ctx, hook.JobID, nil)
		if err != nil {
			return "", err
		}
		if spec.Spec.Options != nil {
			keys := make([]string, 0, len(mapped))
			for k := range mapped {
				keys = append(keys, k)
			}
			if bad := engine.SecretOptionKeys(spec.Spec.Options, keys); len(bad) > 0 {
				fields := make([]ProblemDetail, len(bad))
				for i, k := range bad {
					fields[i] = ProblemDetail{Message: "a secret option is set by the webhook's static options only", Location: "options." + k}
				}
				return "", invalid("the options mapping may not set secret options", fields...)
			}
		}
		for k, v := range mapped {
			opts[k] = v
		}
	}
	nr, _, err := h.d.prepareRun(ctx, hook.JobID, nil, opts, Trigger{Type: TriggerWebhook, WebhookID: hook.ID})
	if err != nil {
		return "", err
	}
	var runID string
	err = h.d.inTx(ctx, func(tx store.Tx) (string, error) {
		tree, err := tx.CreateRun(ctx, nr, h.d.now())
		if err != nil {
			return "", err
		}
		runID = tree.ID
		return runID, tx.TouchWebhook(ctx, hook.ID, h.d.now())
	})
	return runID, err
}

// mappingReason names the kind of a mapping failure without the error text,
// which can quote the request.
func mappingReason(err error) string {
	var inv *invalidError
	if errors.As(err, &inv) && strings.Contains(inv.detail, "invalid") {
		return "does not compile"
	}
	return "evaluation failed"
}

func (h *hookHandler) mapOptions(src string, req expr.Request) (map[string]any, error) {
	prog, err := h.env.Compile(src)
	if err != nil {
		return nil, invalid("the options mapping is invalid")
	}
	m, err := h.env.EvalMapping(prog, req)
	if err != nil {
		// The text of an evaluation error can quote request content: say only
		// that the mapping failed.
		return nil, invalid("the options mapping could not be applied to this request")
	}
	return m, nil
}

// readHookRequest reads the body (JSON when the content type says so, else the
// text), the headers without credentials, and the query.
func readHookRequest(w http.ResponseWriter, r *http.Request) (expr.Request, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	if err != nil {
		return expr.Request{}, invalid("the request body is too large or unreadable")
	}
	req := expr.Request{Headers: map[string]any{}, Query: map[string]any{}}
	for k, v := range r.Header {
		if lk := strings.ToLower(k); !credentialHeaders[lk] && len(v) > 0 {
			req.Headers[lk] = v[0]
		}
	}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			req.Query[k] = v[0]
		}
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	switch {
	case len(raw) == 0:
		req.Body = nil
	case strings.Contains(ct, "json"):
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return expr.Request{}, invalid("the request body is not valid JSON")
		}
		req.Body = v
	case utf8.Valid(raw):
		req.Body = string(raw)
	default:
		return expr.Request{}, invalid("the request body is not text")
	}
	return req, nil
}

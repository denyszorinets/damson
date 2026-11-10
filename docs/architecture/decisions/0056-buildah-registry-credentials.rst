ADR-0056: Buildah registry credentials
======================================

:Status: Accepted
:Date: 2026-11-07

Issue :issue:`182`. Clarifies ARCHITECTURE section 6.5.

Context
-------

ARCHITECTURE 6.5 listed ``registry_credentials`` as "a list of secret refs of type
``registry``". There is no ``registry`` secret type (the types are ``generic``,
``password``, ``ssh_key`` and ``token``), and a bare list of refs does not say
which registry a login is for, which a registry auth file needs.

Decision
--------

``registry_credentials`` is a list of ``{registry, credential}``. ``registry`` is
a host (with a port when it is not the default) and ``credential`` is a
``secret://`` ref of a ``password`` secret (``username``, ``password``). Each
registry may appear once. The plugin writes them to a registry auth file with
mode 0600 (``REGISTRY_AUTH_FILE``) in the step's credential directory and removes
it afterwards. Nothing secret is on a command line.

Alternatives Considered
-----------------------

- *A new ``registry`` secret type with a ``registry`` field.* It changes the
  secret schema and the secrets UI for no gain: the registry host belongs to the
  step that uses the login, and one ``password`` secret can serve several.
- *Reading the agent user's own logins.* Rejected: it lets any job use the agent
  user's identity.

Decision Criteria
-----------------

No schema change; explicit which login is for which registry; least privilege.

Rationale
---------

The pair keeps the secret types unchanged and makes the auth file trivial to
build.

Consequences
------------

Positive
~~~~~~~~

- Existing ``password`` secrets work as registry logins.

Negative
~~~~~~~~

- The registry host is repeated in every job that uses the login.

Risks
~~~~~

- The job spec shape is part of the plugin's input schema; changing it later is a
  breaking change for jobs.

Follow-up
~~~~~~~~~

- Helm's ``registry_credentials`` for OCI charts uses the same shape.

Validation
----------

The buildah tests check the auth file content, that no password reaches a command
line or the environment, and the input validation.

References
----------

- ARCHITECTURE section 6.5
- :doc:`/reference/plugin-buildah`

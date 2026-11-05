import { api, type components } from '$lib/api/client';
import { check, headers, problemMessage } from '$lib/admin';

export { problemMessage };

export type SecretMeta = components['schemas']['SecretMeta'];
export type SecretType = components['schemas']['Type'];

export { Unauthorized, loadToken, saveToken } from '$lib/admin';

export async function fetchSecrets(token: string, signal?: AbortSignal): Promise<SecretMeta[]> {
	const res = await api.GET('/secrets', {
		params: { query: { limit: 200 } },
		headers: headers(token),
		signal
	});
	check(res);
	return res.data?.items ?? [];
}

export async function fetchSecretTypes(signal?: AbortSignal): Promise<SecretType[]> {
	const { data, error } = await api.GET('/secret-types', { signal });
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return data.items ?? [];
}

/** A field being edited. Values are cleared by the page after every submit. */
export interface FieldRow {
	name: string;
	value: string;
	required: boolean;
	multiline: boolean;
}

/** Fresh rows for a type: its fields, or one empty row for the generic type. */
export function rowsFor(type: SecretType | undefined): FieldRow[] {
	if (!type || !type.fields?.length) {
		return [{ name: '', value: '', required: false, multiline: false }];
	}
	return type.fields.map((f) => ({
		name: f.name,
		value: '',
		required: f.required,
		multiline: f.multiline
	}));
}

/** The request fields: rows with a name and a value (an unset optional field is left out). */
export function fieldsOf(rows: FieldRow[]): Record<string, string> {
	const out: Record<string, string> = {};
	for (const r of rows) {
		if (r.name.trim() && r.value !== '') out[r.name.trim()] = r.value;
	}
	return out;
}

export async function putSecret(
	token: string,
	scope: string,
	name: string,
	type: string,
	rows: FieldRow[],
	ifVersion?: number
): Promise<SecretMeta> {
	const res = await api.PUT('/secrets/{scope}/{name}', {
		params: { path: { scope, name } },
		headers: headers(token),
		body: { type, fields: fieldsOf(rows), if_version: ifVersion }
	});
	check(res);
	if (!res.data) throw new Error('Empty response');
	return res.data;
}

export async function deleteSecret(token: string, scope: string, name: string): Promise<void> {
	const res = await api.DELETE('/secrets/{scope}/{name}', {
		params: { path: { scope, name } },
		headers: headers(token)
	});
	check(res);
}

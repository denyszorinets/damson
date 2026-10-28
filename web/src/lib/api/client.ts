import createClient from 'openapi-fetch';
import type { paths } from './schema';

/**
 * The typed API client. Types come from the controller's OpenAPI document
 * (make gen); do not write fetch calls by hand.
 */
export const api = createClient<paths>({ baseUrl: '/api/v1' });

export type { components } from './schema';

import { describe, expect, it } from 'vitest';
import { OptionsRejected, optionErrors } from './triggers';

describe('optionErrors', () => {
	it('groups located errors by option', () => {
		const e = optionErrors({
			errors: [
				{ message: 'not allowed', location: 'options.env' },
				{ message: 'too short', location: 'options.version.0' },
				{ message: 'env is required', location: 'options' },
				{ message: 'unrelated', location: 'body.version' }
			]
		});
		expect(e).toBeInstanceOf(OptionsRejected);
		expect(e?.fields).toEqual({ env: ['not allowed'], version: ['too short'] });
		expect(e?.general).toEqual(['env is required']);
	});

	it('treats a job without options as a general error', () => {
		const e = optionErrors({ errors: [{ message: 'not accepted', location: 'body.options' }] });
		expect(e?.general).toEqual(['not accepted']);
	});

	it('is null when nothing concerns the options', () => {
		expect(optionErrors(undefined)).toBeNull();
		expect(optionErrors({ errors: null })).toBeNull();
		expect(optionErrors({ errors: [{ message: 'x', location: 'body.version' }] })).toBeNull();
	});
});

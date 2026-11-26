import type { IExecuteFunctions, IPollFunctions } from 'n8n-workflow';
import { connection } from './connection';

export async function clrdClient(context: IExecuteFunctions | IPollFunctions) {
	const credentials = await context.getCredentials<{ url: string; token: string }>('clrdApi');
	const { url, token } = connection(credentials.url, credentials.token);
	const { Client } = await import('../client.mjs');
	return new Client({
		url,
		token,
		fetch: async (input, init) => {
			// Use n8n's network policy, while the SDK owns the clrd routes and bodies.
			const response = await context.helpers.httpRequest({
				url: String(input),
				method: init?.method as 'GET' | 'POST',
				headers: init?.headers as Record<string, string>,
				body: init?.body,
				returnFullResponse: true,
				ignoreHttpStatusErrors: true,
				disableFollowRedirect: true,
				timeout: 10_000,
			}).catch(() => {
				throw new Error('Could not reach clrd. Check the server URL and network access.');
			});
			return new Response(
				[204, 205, 304].includes(response.statusCode) ? null :
					typeof response.body === 'string' ? response.body : JSON.stringify(response.body),
				{ status: response.statusCode },
			);
		},
	});
}

import type { ICredentialTestFunction, ICredentialType, INodeProperties } from 'n8n-workflow';
import { connection } from '../nodes/connection';

export class ClrdApi implements ICredentialType {
	name = 'clrdApi';

	displayName = 'clrd API';

	icon = { light: 'file:../nodes/clrd.svg', dark: 'file:../nodes/clrd.dark.svg' } as const;

	documentationUrl = 'https://github.com/thejdubb02/clrd/blob/main/docs/integrations/n8n.md';

	test: ICredentialTestFunction = async function (credential) {
		let config;
		try {
			config = connection(String(credential.data?.url ?? ''), String(credential.data?.token ?? ''));
		} catch (error) {
			return { status: 'Error', message: (error as Error).message };
		}
		const { url, token } = config;
		try {
			// Credential tests currently expose only this request helper.
			// eslint-disable-next-line @n8n/community-nodes/no-deprecated-workflow-functions
			const result = await this.helpers.request({
				uri: url + '/api/v1/counts',
				headers: { Authorization: 'Bearer ' + token },
				followRedirect: false,
				simple: false,
				resolveWithFullResponse: true,
				json: true,
				timeout: 10_000,
			});
			// A publish-only token authenticates but cannot read counts.
			if (result.statusCode === 200 && Number.isInteger(result.body?.inbox) ||
				result.statusCode === 403 && result.body?.code === 'forbidden' &&
				result.body?.error === 'token lacks the read scope') {
				return { status: 'OK', message: 'Connected to clrd' };
			}
		} catch {
			// Request errors can contain headers. Return a fixed message.
		}
		return { status: 'Error', message: 'Check the clrd server URL and API token' };
	};

	properties: INodeProperties[] = [
		{
			displayName: 'Server URL',
			name: 'url',
			type: 'string',
			default: '',
			placeholder: 'https://clrd.example.com',
			required: true,
		},
		{
			displayName: 'API Token',
			name: 'token',
			type: 'string',
			typeOptions: { password: true },
			default: '',
			required: true,
		},
	];
}

import type {
	IDataObject,
	IExecuteFunctions,
	INodeExecutionData,
	INodeType,
	NodeConnectionType,
	INodeTypeDescription,
} from 'n8n-workflow';
import type { SendOptions } from '../../js/index.js' with { 'resolution-mode': 'import' };
import { clrdClient } from './client';

const operations = ['send', 'clear', 'acknowledge', 'resolve', 'snooze'] as const;
type Operation = (typeof operations)[number];

export class Clrd implements INodeType {
	description: INodeTypeDescription = {
		displayName: 'clrd',
		name: 'clrd',
		subtitle: '={{$parameter["operation"]}}',
		usableAsTool: true,
		icon: { light: 'file:clrd.svg', dark: 'file:clrd.dark.svg' },
		group: ['output'],
		version: 1,
		description: 'Send or change the state of a clrd notification',
		defaults: { name: 'clrd' },
		inputs: ['main' as NodeConnectionType],
		outputs: ['main' as NodeConnectionType],
		credentials: [{ name: 'clrdApi', required: true }],
		properties: [
			{
				displayName: 'Operation',
				name: 'operation',
				type: 'options',
				noDataExpression: true,
				options: [
					{ name: 'Acknowledge', value: 'acknowledge', action: 'Acknowledge a notification' },
					{ name: 'Clear', value: 'clear', action: 'Clear a notification' },
					{ name: 'Resolve', value: 'resolve', action: 'Resolve a notification' },
					{ name: 'Send', value: 'send', action: 'Send a notification' },
					{ name: 'Snooze', value: 'snooze', action: 'Snooze a notification' },
				],
				default: 'send',
			},
			{
				displayName: 'Topic',
				name: 'topic',
				type: 'string',
				default: '',
				required: true,
				displayOptions: { show: { operation: ['send'] } },
			},
			{
				displayName: 'Message',
				name: 'message',
				type: 'string',
				typeOptions: { rows: 4 },
				default: '',
				required: true,
				displayOptions: { show: { operation: ['send'] } },
			},
			{
				displayName: 'Additional Fields',
				name: 'additionalFields',
				type: 'collection',
				placeholder: 'Add Field',
				default: {},
				displayOptions: { show: { operation: ['send'] } },
				options: [
					{
						displayName: 'Actions (JSON)',
						name: 'actions',
						type: 'json',
						default: '[]',
						description: 'Array of actions, for example [{"label":"Copy ID","copy":"build-42"}]',
					},
					{
						displayName: 'Attachment Name',
						name: 'attachmentName',
						type: 'string',
						default: '',
						description: 'Optional file name to display for the attachment URL',
					},
					{
						displayName: 'Attachment URL',
						name: 'attachmentUrl',
						type: 'string',
						default: '',
						description: 'HTTP or HTTPS URL of a file to link',
					},
					{
						displayName: 'External ID',
						name: 'id',
						type: 'string',
						default: '',
						description: 'Stable key for updating the same open notification',
					},
					{
						displayName: 'Icon URL',
						name: 'icon',
						type: 'string',
						default: '',
						description: 'HTTP or HTTPS URL of the notification icon',
					},
					{
						displayName: 'Priority',
						name: 'priority',
						type: 'options',
						options: [
							{ name: '1 (Min)', value: 1 },
							{ name: '2 (Low)', value: 2 },
							{ name: '3 (Default)', value: 3 },
							{ name: '4 (High)', value: 4 },
							{ name: '5 (Urgent)', value: 5 },
						],
						default: 3,
					},
					{
						displayName: 'Reopen',
						name: 'reopen',
						type: 'boolean',
						default: false,
						description: 'Whether to make an acknowledged notification active when updating its external ID',
					},
					{ displayName: 'Title', name: 'title', type: 'string', default: '' },
				],
			},
			{
				displayName: 'Notification ID',
				name: 'notificationId',
				type: 'string',
				default: '',
				required: true,
				description: 'ID returned by clrd when the notification was sent',
				displayOptions: { show: { operation: ['acknowledge', 'clear', 'resolve', 'snooze'] } },
			},
			{
				displayName: 'Duration',
				name: 'duration',
				type: 'string',
				default: '1h',
				required: true,
				description: 'How long to snooze, for example 15m or 1h',
				displayOptions: { show: { operation: ['snooze'] } },
			},
		],
	};

	async execute(this: IExecuteFunctions): Promise<INodeExecutionData[][]> {
		const client = await clrdClient(this);
		const output: INodeExecutionData[] = [];
		const items = this.getInputData();
		for (let i = 0; i < items.length; i++) {
			try {
				const operation = this.getNodeParameter('operation', i) as Operation;
				let result;
				if (operation === 'send') {
					const topic = this.getNodeParameter('topic', i);
					const message = this.getNodeParameter('message', i);
					if (typeof topic !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/.test(topic)) {
						throw new Error('Enter a topic using 1 to 64 letters, digits, underscores or hyphens');
					}
					if (typeof message !== 'string' || !message.trim()) {
						throw new Error('Enter a notification message');
					}
					const fields = this.getNodeParameter('additionalFields', i, {}) as Omit<SendOptions, 'actions'> & {
						attachmentUrl?: string;
						attachmentName?: string;
						actions?: string | SendOptions['actions'];
					};
					const { attachmentUrl, attachmentName, actions, ...options } = fields ?? {};
					if (attachmentName && !attachmentUrl) throw new Error('Enter an attachment URL with the attachment name');
					const parsed: unknown = typeof actions === 'string' ? JSON.parse(actions) : actions;
					if (parsed !== undefined && !Array.isArray(parsed)) throw new Error('Actions must be a JSON array');
					result = await client.send(topic, message, {
						...options,
						...(attachmentUrl && { attachment: { url: attachmentUrl, name: attachmentName || undefined } }),
						actions: parsed as SendOptions['actions'],
					});
				} else {
					const id = this.getNodeParameter('notificationId', i);
					if (typeof id !== 'string' || !id || /^(?:\.{1,2})$|\s/.test(id)) {
						throw new Error('Enter a valid notification ID');
					}
					const actions = {
						acknowledge: () => client.ack(id),
						clear: () => client.dismiss(id),
						resolve: () => client.resolve(id),
						snooze: () => client.snooze(id, this.getNodeParameter('duration', i) as string),
					};
					if (!Object.hasOwn(actions, operation)) throw new Error('Select a clrd operation');
					result = await actions[operation]();
				}
				output.push({ json: { ...result } as IDataObject, pairedItem: i });
			} catch (error) {
				const failure = error instanceof Error ? error : new Error(String(error));
				if (!this.continueOnFail()) {
					// Report the item without importing a second copy of the host workflow library.
					// eslint-disable-next-line @n8n/community-nodes/require-node-api-error
					throw new Error(`Item ${i + 1}: ${failure.message}`);
				}
				output.push({ json: { error: failure.message }, pairedItem: i });
			}
		}
		return [output];
	}
}

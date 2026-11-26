import type {
	IDataObject,
	IExecuteFunctions,
	INodeExecutionData,
	INodeType,
	NodeConnectionType,
	INodeTypeDescription,
} from 'n8n-workflow';
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
						displayName: 'External ID',
						name: 'id',
						type: 'string',
						default: '',
						description: 'Stable key for updating the same open notification',
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
					const topic = this.getNodeParameter('topic', i) as string;
					const message = this.getNodeParameter('message', i) as string;
					const fields = this.getNodeParameter('additionalFields', i);
					result = await client.send(topic, message, fields);
				} else {
					const id = this.getNodeParameter('notificationId', i) as string;
					const actions = {
						acknowledge: () => client.ack(id),
						clear: () => client.dismiss(id),
						resolve: () => client.resolve(id),
						snooze: () => client.snooze(id, this.getNodeParameter('duration', i) as string),
					};
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

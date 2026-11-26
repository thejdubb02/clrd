import type {
	IDataObject,
	INodeExecutionData,
	INodeType,
	NodeConnectionType,
	INodeTypeDescription,
	IPollFunctions,
} from 'n8n-workflow';
import type { EventCursor } from '../../js/index.js' with { 'resolution-mode': 'import' };
import { clrdClient } from './client';

export class ClrdTrigger implements INodeType {
	description: INodeTypeDescription = {
		displayName: 'clrd Trigger',
		name: 'clrdTrigger',
		subtitle: '={{$parameter["event"] === "created" ? "New notification" : "State change"}}',
		icon: { light: 'file:clrd.svg', dark: 'file:clrd.dark.svg' },
		group: ['trigger'],
		version: 1,
		description: 'Start a workflow when a clrd notification arrives or changes state',
		defaults: { name: 'clrd Trigger' },
		polling: true,
		inputs: [],
		outputs: ['main' as NodeConnectionType],
		credentials: [{ name: 'clrdApi', required: true }],
		properties: [
			{
				displayName: 'Event',
				name: 'event',
				type: 'options',
				options: [
					{ name: 'New Notification', value: 'created' },
					{ name: 'State Change', value: 'state' },
				],
				default: 'created',
			},
			{
				displayName: 'Topic',
				name: 'topic',
				type: 'string',
				default: '',
				description: 'Leave empty to watch all topics the token can read',
			},
		],
	};

	async poll(this: IPollFunctions): Promise<INodeExecutionData[][] | null> {
		const client = await clrdClient(this);
		const topic = this.getNodeParameter('topic') as string;
		const type = this.getNodeParameter('event') as string;
		if (this.getMode() === 'manual') {
			const { notifications } = await client.list({ topic, state: 'all', limit: 1 }) as {
				notifications: IDataObject[];
			};
			if (!notifications.length) return null;
			const notification = notifications[0];
			return [[{ json: { seq: notification.seq, type: 'sample', notification } }]];
		}
		const data = this.getWorkflowStaticData('node');
		const key = JSON.stringify([client.url, topic, type]);
		// n8n saves static data after a successful execution. A later event may
		// therefore replay after a restart even if it was seen by this poll.
		const previous = data.key === key ? data.cursor as unknown as EventCursor : undefined;
		const { events, cursor, reset } = await client.pollEvents(previous, { topic });
		data.key = key;
		data.cursor = { ...cursor };
		if (reset) {
			this.logger.warn('clrd event history reset; resuming from the current head');
		}
		const output = events
			.filter((event) => event.type === type && event.notification)
			.map((event) => ({ json: { ...event } as IDataObject }));
		return output.length ? [output] : null;
	}
}

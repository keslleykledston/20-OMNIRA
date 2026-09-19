// Compatibility exports for code outside the integrations page. New code must
// use the provider-neutral client.
export { integrationsAPI as channelsAPI, integrationErrorMessage as channelErrorMessage } from './integrations';
export type { ChannelConnection, QRImage } from './integrations';

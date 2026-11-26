export function connection(urlValue: string, tokenValue: string) {
	const value = urlValue?.trim();
	const url = value && URL.canParse(value) ? new URL(value) : null;
	const token = tokenValue?.trim();
	if (!url || !['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
		throw new Error('Enter the clrd HTTP or HTTPS server URL without credentials, query or fragment');
	}
	if (!token) throw new Error('Enter a clrd API token');
	return { url: url.href.replace(/\/+$/, ''), token };
}

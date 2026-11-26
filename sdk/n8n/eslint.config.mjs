import community from '@n8n/eslint-plugin-community-nodes';
import parser from '@typescript-eslint/parser';

export default [
	{ ignores: ['dist/'] },
	{ files: ['**/*.ts', 'package.json'], languageOptions: { parser } },
	community.configs.recommended,
];

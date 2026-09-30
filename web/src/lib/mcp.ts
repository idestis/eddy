/** The command that registers the hub as an MCP server in Claude Code. */
export const claudeCommand = (origin: string, token: string): string =>
  `claude mcp add --transport http eddy ${origin}/mcp --header "Authorization: Bearer ${token}"`;

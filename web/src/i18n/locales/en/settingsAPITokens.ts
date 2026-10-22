export default {
  title: 'MCP endpoints',
  desc: 'Access points for machine clients: an AI client creates projects, configures pipelines and runs builds over MCP, or a script calls the platform API directly. One endpoint = its own URL + one token + two dials (which tools it may touch, which groups it may see). It acts on behalf of one specific account, with exactly the permissions that person has when signing in. The platform stores only a digest — the plaintext is handed out once, when the endpoint is created, and can never be read back.',
  issue: 'New endpoint',
  retry: 'Retry',
  listTitle: 'Endpoints',
  tokenCount: '{n}',
  emptyList: 'No MCP endpoints yet. Create one, then put its URL and token into your AI client’s MCP configuration.',
  metaOwner: 'Owner {owner}',
  metaCreatedBy: 'Issued by {who}',
  metaCreated: 'Created {at}',
  metaLastUsed: 'Last used {at}',
  metaTools: 'Tools {scope}',
  metaGroups: 'Groups {scope}',
  scopeToolsAll: 'unrestricted',
  scopeToolsSome: 'only {n}',
  scopeGroupsAll: 'unrestricted',
  scopeGroupsSome: 'only {names}',
  neverUsed: 'Never used',
  revoke: 'Revoke',
  revokeTitle: 'Revoke endpoint',
  revokeConfirm:
    'Revoke the endpoint {name}? Revoking deletes the row: the next request carrying it gets a straight 401 and this URL stops working altogether. This cannot be undone.',
  confirmRevoke: 'Revoke',
  revoking: 'Revoking…',
  cancel: 'Cancel',
  done: 'Saved it — close',
  copy: 'Copy',
  copied: 'Copied',
  modalTitle: 'New MCP endpoint',
  issuing: 'Creating…',
  fieldName: 'Endpoint name',
  nameHint: 'A human-readable label saying which machine or client this is (max {max} characters).',
  fieldOwner: 'Owner',
  ownerSelf: 'Myself',
  ownerHint:
    'The token acts as this account — when someone later asks “who ran builds with it”, this is the column they read. Leave the default “Myself” to issue it to the signed-in user.',
  fieldTools: 'Allowed tools',
  preset: {
    all: 'All tools',
    readonly: 'Read-only',
    ops: 'Read-only + run',
  },
  presetCustom: 'Custom selection',
  tagReadOnly: 'read-only',
  toolsHintAll: 'Nothing ticked = no limit: every MCP tool the platform offers stays available.',
  toolsHintSome:
    'Only the {n} of {total} ticked tools are exposed: the client’s tools/list shows nothing else, and an out-of-range call is refused outright.',
  fieldGroups: 'Visible groups',
  groupProjectCount: '{n} projects',
  noGroups: 'There are no groups yet. Create one under “Groups & permissions” first — otherwise a group-limited endpoint can only ever see nothing.',
  groupsHintAll: 'Nothing ticked = no limit: the token sees everything its owner can see.',
  groupsHintSome:
    'Only “ticked groups ∩ what the owner can already see” is exposed. Note that limiting groups also hides ungrouped projects — even when the owner is an admin.',
  plaintextLabel: 'Token plaintext',
  issuedTitle: 'Endpoint created',
  issuedOnce:
    'The plaintext is shown exactly once: the database holds only a digest, so once this dialog closes the platform can never display it again. Copy it now — otherwise the only option left is revoke and reissue.',
  scopeEcho: 'This endpoint’s scope: tools {tools} · groups {groups}',
  endpointLabel: 'MCP endpoint URL',
  howToTitle: 'Then wire it into your AI client — pick the tab for the client you use and copy it:',
  client: {
    cursor: 'Cursor',
    vscode: 'VS Code',
    claudeCode: 'Claude Code',
    claudeDesktop: 'Claude Desktop',
    generic: 'Other clients',
  },
  snippetWhere: {
    cursor: 'Paste into Cursor’s mcp.json (or Settings → MCP Servers → Add)',
    vscode: 'Paste into the project’s .vscode/mcp.json — VS Code uses the “servers” root key, not “mcpServers”',
    claudeCode: 'Paste into a .mcp.json at the project root',
    claudeCodeCli: 'Or add it with one command (same result):',
    claudeDesktop:
      'Claude Desktop’s config only accepts a local stdio server: paste into claude_desktop_config.json, which requires the pipewright binary on this machine',
    generic: 'Generic mcpServers shape: for other clients that support remote HTTP, adjust the key names',
  },
  snippetHint:
    'Restart the client afterwards. Both forms expose the same tools with the same token: the stdio one launches a local process that connects out to the platform (use it when the platform sits behind a NAT and the machine opens no port), the HTTP one connects straight to this endpoint’s own URL. Keep the token in env rather than command-line arguments — anyone on the same machine can read command lines from ps.',
  errNameEmpty: 'Please enter an endpoint name',
  errNameTooLong: 'Endpoint name too long (max {max} characters)',
  errLoadConn: 'Cannot reach the service. Check the network and try again.',
  errLoadStatus: 'Failed to load the endpoint list (HTTP {status})',
  errLoadRetry: 'Failed to load the endpoint list. Please retry.',
  errConnRetry: 'Cannot reach the service. Check the network and try again.',
  errIssueStatus: 'Failed to create the endpoint (HTTP {status})',
  errIssueRetry: 'Failed to create the endpoint. Please retry.',
  errRevokeStatus: 'Failed to revoke the endpoint (HTTP {status})',
  errRevokeRetry: 'Failed to revoke the endpoint. Please retry.',
  editScope: 'Edit scope',
  editTitle: 'Edit endpoint scope',
  editWho:
    'Changing the scope leaves the token alone: the URL and the plaintext stay as they are, so no client needs reconfiguring.',
  editToolsHintAll:
    'Nothing ticked = back to unrestricted: saving exposes every MCP tool the platform offers on this endpoint.',
  editGroupsHintAll:
    'Nothing ticked = back to unrestricted: saving lets this endpoint see everything its owner can already see.',
  editEffectHint:
    'Saving takes effect immediately: the next call from the client follows the new scope — no restart, no new token.',
  save: 'Save scope',
  saving: 'Saving…',
  optionGone: 'no longer exists',
  endpointOwnHint: 'This URL only accepts the token it was created with; any other token gets a 403.',
  errEditStatus: 'Failed to save the scope (HTTP {status})',
  errEditRetry: 'Failed to save the scope. Please retry.',
  guide: {
    cta: 'Usage guide',
    open: 'View the full guide',
    title: 'Pipewright MCP endpoint — usage guide',
    hint: 'Copy the whole block and hand it to an AI client or a colleague: the URL, the token, which tools are allowed and the gotchas are all inside.',
    tokenMissing:
      'The plaintext token is only shown when the endpoint is created, so the block below carries the placeholder {placeholder} — swap in your own token before pasting it anywhere.',
    tokenPlaceholder: '<paste the token here>',
    intro:
      'This is a Pipewright MCP endpoint. Pipewright is a self-hosted CI/CD platform covering projects, pipelines, builds and releases; through this endpoint you can list projects, configure a pipeline, trigger a build, read run status and logs, and cancel a run. The token acts on behalf of {owner}, with the same permissions that person has when signing in, and this endpoint narrows them further (see "Allowed scope").',
    connectTitle: 'How to connect',
    httpNote:
      'Option 1 — HTTP, direct (recommended). Merge this into the MCP config of your client: Cursor mcp.json and Claude Code .mcp.json use the mcpServers root key, VS Code .vscode/mcp.json uses servers:',
    stdioNote:
      'Option 2 — local stdio bridge. Use it when the pipewright binary is installed on this machine and the platform sits on an intranet you do not want to expose:',
    rawNote:
      'Option 3 — raw JSON-RPC (scripts and debugging). POST to the endpoint URL with Authorization: Bearer plus the token, Content-Type: application/json and Accept: application/json, text/event-stream; call initialize first, then tools/list, then tools/call. Example:',
    scopeTitle: 'Allowed scope',
    toolsAll: 'Tools: all {n} are exposed.',
    toolsSome: 'Tools: only {n} of {total} are exposed.',
    groupsAll: 'Groups: no limit — every project the owner can already see stays visible.',
    groupsSome:
      'Groups: only these groups are visible — {names}. Ungrouped projects fall outside it, even when the owner is an admin.',
    scopeNote:
      'Both scopes only intersect and only ever narrow: tools outside the list do not show up in tools/list, and calling them anyway is refused — that is not a client misconfiguration.',
    rulesTitle: 'Conventions and gotchas',
    ruleOnce:
      'The plaintext token appears once, at creation; the platform stores only a digest and can never return it. If it is lost, revoke and issue a new one instead of digging through repositories or tickets.',
    ruleBaseURL:
      'For the stdio form, PIPEWRIGHT_URL must be the site root, not this endpoint URL — the tools call back into the platform REST API by appending /api/..., and an endpoint URL breaks that path.',
    ruleWrite:
      'trigger_run, cancel_run, create_project and set_pipeline really change platform state; confirm the project, the branch and the blast radius with the user first.',
    ruleLogs: 'Build logs can be long — read the tail with get_run_logs instead of pulling the whole output into context.',
    rulePlain:
      'Over plain http the token travels unencrypted; use https for anything public, or keep the endpoint inside a trusted network.',
    tagWrite: 'write',
  },
}

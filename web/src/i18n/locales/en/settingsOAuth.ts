export default {
  title: 'OAuth Apps',
  sectionDesc:
    'Register an OAuth app (Client ID / Secret) for each git platform so users can one-click "Connect" their account from the Credential Vault to automatically obtain a token, without manually pasting a PAT. The secret is masked on storage and cannot be read back after it is written. The same platform can also be registered for a second purpose: letting visitors sign into this instance with their platform account. DingTalk, Feishu, WeCom and generic OIDC only ever exist for that second purpose — they never appear in the Credential Vault.',
  tabsAria: 'OAuth provider tabs',
  retry: 'Retry',

  providerCustomLabel: 'Self-hosted',
  providerGiteeDesc: 'Gitee',
  providerCustomDesc: 'Self-hosted GitLab / Gitea, etc.',

  statusEnabled: 'Enabled',
  statusConfiguredIdle: 'Configured · Not enabled',
  statusUnconfigured: 'Not configured',

  clientIdPlaceholder: 'Get it from the OAuth app page of the git platform',
  clientIdPlaceholderOidc: 'Issued when you register an OIDC client at your identity provider',
  clientIdPlaceholderPlatform:
    'The Client ID from the open-platform app entry (for WeCom, the corp ID instead)',
  secretOptional: '(only the mask is shown after writing)',
  secretPlaceholderKeep: 'Leave blank to keep the existing secret',
  secretPlaceholderNew: 'Paste Client Secret…',
  secretStored: 'Stored: {masked}',

  toggleAria: 'Enable {provider} OAuth',
  toggleTitle: 'Enable connection',
  toggleDesc: 'Once enabled, this platform\'s "Connect" button appears in the Credential Vault',

  lastSaved: 'Last saved {time}',
  saveBtn: 'Save',

  errNoServer: 'Unable to reach the server. Please check that the backend is running and retry',
  errVaultUnconfigured: 'The vault has no master key configured. Please set the PIPEWRIGHT_MASTER_KEY environment variable',
  errLoadStatus: 'Failed to load ({status})',
  errLoadGeneric: 'Failed to load OAuth apps. Please try again later',

  errClientIdRequiredEnabled: 'Client ID is required when enabled',
  errSecretRequiredEnabled: 'A Client Secret is required when enabled',
  errBaseUrlRequiredCustom: 'A self-hosted instance requires a Base URL',
  errClientIdRequired: 'Please enter the Client ID',
  errBaseUrlRequired: 'Please enter the Base URL',

  toastSaveFailed: 'Failed to save',
  toastSaved: 'OAuth app saved',
  errNoServerShort: 'Unable to reach the server',
  unknownError: 'Unknown error',

  justNow: 'just now',
  minutesAgo: '{n} minutes ago',
  hoursAgo: '{n} hours ago',
  daysAgo: '{n} days ago',

  // ─── purpose switch: two configs per platform (repo credentials / platform sign-in) ───
  // Two configs, two callback URLs: an OAuth app normally registers only one
  // callback, so sharing one between the purposes would block the other.
  purposeSwitchAria: 'OAuth purpose',
  purposeVault: 'Repository credentials',
  purposeVaultHint: 'Signed-in users authorize once and the git token is stored in the vault',
  purposeLogin: 'Platform sign-in',
  purposeLoginHint: 'Visitors sign into this instance with their platform account; auto-created accounts get the least role',
  purposeVaultNote:
    'This config only drives the "Connect git account" button. What you register on the platform side is the Callback URL below — change it and update that app too.',
  purposeLoginNote:
    'Once enabled, this provider appears on the sign-in page. Each purpose needs its own OAuth app — one app accepts only one callback URL.',
  callbackHint: 'Paste this into the "callback / Redirect URI" field of the OAuth app on your git platform',
  callbackHintOidc: 'Paste this into the "callback / Redirect URI" field of the OIDC client at your identity provider',
  callbackHintPlatform:
    'Paste this into the "callback / redirect URL" field of the app on the open platform; WeCom also requires this instance domain to be listed as a trusted domain.',
  callbackCopy: 'Click to select all, then copy it to the platform side',
  domainsLabel: 'Email domain allowlist',
  domainsOptional: '(blank = any domain)',
  domainsPlaceholder: 'corp.example.com, hr.example.com',
  domainsHint:
    'Only accounts whose email sits in one of these domains may sign in. With an empty allowlist any email gets an auto-created account (still with the least role).',
  errDomainsInvalid:
    'The email allowlist contains an invalid entry: give the domain alone (e.g. corp.example.com), without scheme, path or spaces',
  toggleTitleLogin: 'Enable sign-in',
  toggleDescLogin: 'Once enabled, the sign-in page shows a "sign in with this platform" button',
  // ─── Sync a credential at sign-in (the four git platforms' sign-in card) ───
  // This decides how much a single sign-in takes away, so it stays off by
  // default and the copy has to name both the wider scope and the recovery path.
  syncTitle: 'Sync a git credential at sign-in',
  syncDesc: 'After signing in with this platform, store the access token from this authorization as a personal git credential on this account',
  syncAria: 'Enable credential sync at sign-in for {provider}',
  syncNote:
    'Turning this on raises what the login asks for: from "identity only" to "read repositories". The platform refreshes the token before it expires; when a refresh is refused the credential is labelled "authorization revoked", and signing in again restores it. The synced credential stays visible to this account only — which credential a project uses is still your choice.',
  errSyncUnsupported:
    'This identity source cannot hand out a token that clones a repository: credential sync at sign-in is open only to git platforms (Gitee / GitHub / GitLab / self-hosted)',

  // ─── OIDC identity source (only ever listed under "Platform sign-in") ──────
  // The backend resolves the endpoints for real when you save: a wrong config
  // would otherwise surface as "the consent page was fine, then it broke on the
  // way back", so failing at save is the kinder place.
  providerOidcDesc: 'Generic OIDC / SSO (Keycloak, Authentik, …)',
  oidcDiscoveryLabel: 'Discovery URL',
  oidcDiscoveryOptional: '(either this or the three endpoints below)',
  oidcDiscoveryPlaceholder: 'https://sso.example.com/realms/my/.well-known/openid-configuration',
  oidcDiscoveryHint:
    'Given, and the authorize / token / userinfo endpoints come from the document. Saving fetches it for real and fails loudly if it cannot.',
  oidcOverrideOptional: '(blank = use the document)',
  oidcAuthorizeLabel: 'Authorization Endpoint',
  oidcTokenLabel: 'Token Endpoint',
  oidcUserinfoLabel: 'Userinfo Endpoint',
  oidcEndpointPlaceholder: 'https://sso.example.com/…',
  oidcEndpointsHint:
    'If the document is unreachable (isolated network, gateway blocks it), fill all three by hand — an explicit value wins over the document.',
  oidcIdClaimLabel: 'ID claim',
  oidcIdClaimOptional: '(blank = sub)',
  oidcIdClaimHint:
    'Which claim identifies the account. Defaults to sub, which the spec requires to be immutable. Use email or username and a later rename or address change lands on somebody else’s account.',
  oidcScopesLabel: 'Scopes',
  oidcScopesOptional: '(blank = openid profile email)',
  oidcScopesPlaceholder: 'openid profile email',
  oidcScopesHint:
    'Space-separated; openid is mandatory. If no email comes back, the email domain allowlist blocks the sign-in.',
  oidcUserinfoNote:
    'Identity is read from the userinfo endpoint: this build verifies neither the id_token signature nor its audience, so connect an internal or HTTPS-only provider.',
  errOidcEndpointsIncomplete:
    'To enable OIDC, give a Discovery URL or fill all three of authorize / token / userinfo',
  errOidcConfigInvalid:
    'Endpoints could not be resolved: wrong URL, unreachable discovery document, or the three endpoints are incomplete (the stored config was left untouched)',

  // ─── DingTalk / Feishu / WeCom (only ever listed under "Platform sign-in") ──
  // Their official endpoints are baked into the backend presets; the settings page
  // only offers overrides, used for an internal proxy or a private deployment.
  providerDingTalkDesc: 'DingTalk QR sign-in',
  providerFeishuDesc: 'Feishu QR sign-in',
  providerWecomDesc: 'WeCom QR sign-in (self-built app)',
  platformOverrideOptional: '(blank = the platform default)',
  platformEndpointPlaceholder: 'https://… (only needed for a proxy or private deployment)',
  platformEndpointsHint:
    'The authorize / token / userinfo addresses default to the official ones and normally stay empty; override them for an internal proxy or private deployment, and an explicit value wins.',
  agentIdPlaceholder: 'The AgentId from the WeCom app details, e.g. 1000002',
  agentIdHint:
    'The QR authorize URL needs it (the parameter is agentid) to resolve a member identity; the Client ID field takes the corp ID, not this number.',
  platformScopesOptional: '(blank = the default)',
  platformScopesPlaceholder: 'space-separated, e.g. contact:user.base:readonly',
  platformScopesHint:
    'DingTalk defaults to openid; Feishu defaults to no scope at all — asking for a permission the app does not have is rejected outright. To gate sign-in on the email allowlist, the platform has to return an email.',
  platformIdentityNote:
    'Accounts are matched only on the stable ID issued by the platform (DingTalk / Feishu use unionId, falling back to openId; WeCom uses corp ID + member userid). Nicknames and emails never identify an account, so a rename cannot collide with somebody else’s.',
  errAgentIdRequired: 'WeCom needs an AgentId (from the app details)',
  errPlatformConfigInvalid:
    'Endpoint overrides must be http(s) URLs without credentials embedded; leave them blank to use the platform defaults (the stored config was left untouched)',
}

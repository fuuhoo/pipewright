export default {
  title: 'OAuth Apps',
  sectionDesc:
    'Register an OAuth app (Client ID / Secret) for each git platform so users can one-click "Connect" their account from the Credential Vault to automatically obtain a token, without manually pasting a PAT. The secret is masked on storage and cannot be read back after it is written. The same platform can also be registered for a second purpose: letting visitors sign into this instance with their platform account.',
  tabsAria: 'OAuth provider tabs',
  retry: 'Retry',

  providerCustomLabel: 'Self-hosted',
  providerGiteeDesc: 'Gitee',
  providerCustomDesc: 'Self-hosted GitLab / Gitea, etc.',

  statusEnabled: 'Enabled',
  statusConfiguredIdle: 'Configured · Not enabled',
  statusUnconfigured: 'Not configured',

  clientIdPlaceholder: 'Get it from the OAuth app page of the git platform',
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
  oidcScopesHint:
    'Space-separated; openid is mandatory. If no email comes back, the email domain allowlist blocks the sign-in.',
  oidcUserinfoNote:
    'Identity is read from the userinfo endpoint: this build verifies neither the id_token signature nor its audience, so connect an internal or HTTPS-only provider.',
  errOidcEndpointsIncomplete:
    'To enable OIDC, give a Discovery URL or fill all three of authorize / token / userinfo',
  errOidcConfigInvalid:
    'Endpoints could not be resolved: wrong URL, unreachable discovery document, or the three endpoints are incomplete (the stored config was left untouched)',
}

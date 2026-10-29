/**
 * English messages. Mirrors the structure of `zh-CN.ts` (the canonical schema).
 */
import type zhCN from './zh-CN'

const en: typeof zhCN = {
  common: {
    refresh: 'Refresh',
    allArrow: 'All →',
    detailArrow: 'Details →',
    close: 'Close',
    aiTokens: 'This call · {prompt} in / {completion} out tokens',
    transfer: {
      available: 'Available',
      selected: 'Selected',
      searchAvailable: 'Search…',
      searchSelected: 'Search selected…',
      add: 'Add',
      remove: 'Remove',
      noMatch: 'No matches',
      emptyAvailable: 'Nothing left to add',
      emptySelected: 'Nothing selected yet',
      prevPage: 'Previous page',
      nextPage: 'Next page',
      pageOf: 'Page {page} of {pages}',
    },
  },

  locale: {
    label: 'Language',
    'zh-CN': '简体中文',
    en: 'English',
  },

  nav: {
    dashboard: 'Overview',
    projects: 'Projects',
    runs: 'Runs',
    library: 'Library',
    environments: 'Environments',
    dora: 'DORA Metrics',
    serverStatus: 'Servers',
    containers: 'Containers',
    proxyOverview: 'Certificates',
    certs: 'Certificate Manager',
    previews: 'Previews',
    anomaly: 'Anomaly Detection',
    notifications: 'Notification log',
    settings: 'Settings',
    logout: 'Log out',
    ariaMain: 'Main navigation',
    ariaBrandHome: 'Pipewright home',
    ariaEnvironments: 'Environment deployment history',
    expand: 'Expand sidebar',
    collapse: 'Collapse sidebar',
    buildEnvs: 'Build environments',
    configProfiles: 'Config profiles',
    permissions: 'Users & access',
  },

  shell: {
    // Top-right identity chip: accessible name for the account.
    identityAria: 'Current account, open account settings',
    logoutTitle: 'Log out?',
    logoutBody: 'You will need to enter your username and password again to return to the console.',
    logoutLocalTitle: 'Logged out locally',
    logoutLocalDetail: 'The server did not respond, but your local session has been cleared.',
    toThemeLight: 'Switch to light',
    toThemeDark: 'Switch to dark',
    themeLight: '◐ Light',
    themeDark: '◑ Dark',
  },

  login: {
    eyebrow: 'Instance online',
    headlineWelcome: 'Welcome back,',
    headlineEnter: 'enter your',
    headlineWord: 'console',
    headlinePunct: '.',
    lede: 'Self-hosted CI/CD and deployment orchestration — AI pinpoints build failures in seconds.',
    formAria: 'Login form',
    account: 'Username',
    password: 'Password',
    showPassword: 'Show password',
    hidePassword: 'Hide password',
    connecting: 'Connecting…',
    enter: 'Enter console',
    trustSelfHosted: '100% self-hosted',
    trustNoEgress: 'No data egress',
    trustSingleBinary: 'Single binary',
    errUsername: 'Please enter your username',
    errPassword: 'Please enter your password',
    errLockout: 'Too many failed attempts. Please try again later.',
    errCredentials: 'Incorrect username or password',
    errCheckCredentials: 'Please check your username and password',
    errNetwork: 'Cannot reach the server. Check that the backend is running and retry.',
    errGeneric: 'Login failed ({status})',
    errUnknown: 'Unknown error, please retry',
    oauthDivider: 'Or sign in with an external account',
    oauthGroupAria: 'Sign in with an external account',
    oauthLoginWith: 'Continue with {provider}',
    resetHelpBtn: 'Forgot admin password?',
    resetHelpTitle: 'Reset the built-in admin password',
    resetHelpSub:
      'Passwords are stored only as one-way hashes, so nobody can recover them. The only way back is one command on the machine that hosts this instance — no login or restart needed.',
    resetHelpLockHint:
      'Being locked out is not the same as forgetting: 5 consecutive failures lock sign-in for 15 minutes, then it clears on its own.',
    resetHelpStep1:
      'Open a shell on the server that runs this instance (for Docker, enter the container).',
    resetHelpStep2:
      'Run one of these from the working directory the service uses (where the database lives):',
    resetHelpLabelBinary: 'Binary / process deployment',
    resetHelpLabelDocker: 'Docker / compose deployment',
    resetHelpStep3:
      'When it reports the reset, sign in with the new password — it takes effect immediately.',
    resetHelpGen:
      '--generate prints a strong password once; to choose your own, use --stdin < password.txt or --password-file <file>.',
    resetHelpCwd:
      'It resets the database resolved from the current directory. In containers that is /data by default — do not cd into a subdirectory first.',
    resetHelpCopy: 'Copy',
    resetHelpCopied: 'Copied',
    resetHelpClose: 'Close',
  },

  // The OAuth callback redirects back with a machine code only (/login?oauth_error=name_taken);
  // this namespace turns those codes into readable reasons. Keys must match the enumeration in
  // web/src/lib/oauthReason.ts verbatim — a missing key surfaces as a raw key or the fallback.
  oauthErr: {
    generic: 'Sign-in did not complete ({code})',
    invalidState: 'The sign-in check failed. Return to the login page and start again.',
    missingCode: 'The platform returned no authorization code. Please start again.',
    providerDenied: 'You cancelled the authorization on the platform.',
    scopeRejected: 'The platform refused the permissions this sign-in asked for. Most often the OAuth app has no repository read scope registered, so retrying will not help: ask an administrator to add it, or turn off "Sync a git credential at sign-in".',
    appNotConfigured: "Sign-in through this platform isn't fully configured. Contact an administrator.",
    invalidProvider: 'Unrecognized sign-in provider.',
    vaultUnavailable: 'The server could not decrypt the sign-in configuration. Contact an administrator.',
    authorizeFailed: 'Could not reach that platform. Please try again later.',
    exchangeFailed: 'Exchanging the authorization code failed. Please start again.',
    identityUnavailable: "Couldn't read your account on that platform. Please start again.",
    domainNotAllowed: 'That email domain is not allowed to sign in on this instance.',
    oidcConfigInvalid: 'This sign-in entry has a broken endpoint configuration (the discovery document is unreachable or the endpoints are incomplete). Contact an administrator.',
    nameTaken: 'That username is already taken here. Ask an administrator to bind the accounts instead.',
    accountUnavailable: 'The bound account is no longer available. Contact an administrator.',
    roleUnavailable: 'The instance has no default role for new accounts. Contact an administrator.',
    loginDisabled: 'Sign-in through this platform has been switched off.',
    alreadyBound: 'That platform account is already bound to another user.',
    providerBound: 'This account is already bound to that platform. Unbind it first to switch.',
    sessionUnavailable: 'Could not create a session. Please try again.',
    loginUnavailable: 'Platform sign-in is not enabled on this instance. Contact an administrator.',
    loginFailed: 'Sign-in did not complete. Try again, or use your username and password.',
    oauthUnavailable: 'OAuth is not configured on this instance. Contact an administrator.',
    sessionUnbound: 'Your session is no longer valid. Sign in again before binding.',
  },

  dashboard: {
    title: 'Overview',
    subtitle: 'CI/CD · Deploy · Ops at a glance',
    newProject: '＋ New project',
    kpiAria: 'Key metrics',
    kpiProjects: 'Projects',
    kpiRuns: 'Total runs',
    kpiSuccess: 'Recent success rate',
    kpiSuccessSuffix: 'last 12',
    kpiRunning: 'In progress',
    kpiServers: 'Servers online',
    doraTitle: 'DORA Metrics',
    doraDeployFreq: 'Deployment Frequency',
    doraLeadTime: 'Lead Time for Changes',
    doraCfr: 'Change Failure Rate',
    doraMttr: 'Mean Time to Restore',
    // English title already carries the canonical name → subtitle hidden.
    subDeployFreq: '',
    subLeadTime: '',
    subCfr: '',
    subMttr: '',
    capDeployments: '{count} deployments in {days} days',
    capLeadTime: 'Median time from commit to deploy',
    capCfr: '{failed}/{total} deployments failed',
    capMttr: 'Median time from failure to recovery',
    doraEmpty:
      'No DORA data yet. Once a few deployments complete, the four engineering-performance metrics will be summarized here.',
  },

  dash: {
    recentRuns: 'Recent runs',
    recentRunsEmpty:
      'No runs yet. Once you connect a project and trigger a pipeline, recent builds/deploys appear here.',
    servers: 'Server health',
    serversEmpty:
      'No servers registered yet. Add target hosts on the Servers page to see live CPU/memory/disk here.',
    online: 'Online',
    offline: 'Offline',
    memory: 'Memory',
    disk: 'Disk',
    load: 'Load',
    unreachable: 'Unreachable',
    environments: 'Environment status',
    envEmpty:
      'No environment deployments yet. Configure branch→environment mapping and deploy to see each environment’s current version here.',
    deployed: 'Deployed',
    notDeployed: 'Not deployed',
    alertsTitle: 'Alerts & AI diagnosis',
    anomalyLink: 'Anomaly →',
    resourceAnomalies: 'Resource anomalies',
    noAnomalies: 'No anomalies · all hosts within thresholds ✓',
    aiDiagLoop: 'AI failure-diagnosis loop',
    noDiagFeedback:
      'No diagnosis feedback yet. Rate failure diagnoses with 👍/👎 and accuracy will accrue here.',
    accuracy: 'Accuracy',
    feedbackTotal: '{n} feedback total',
  },

  runStatus: {
    queued: 'Queued',
    running: 'Running',
    waiting_approval: 'Awaiting approval',
    success: 'Success',
    failed: 'Failed',
    partial_failed: 'Partial failure',
    rolled_back: 'Rolled back',
  },

  time: {
    justNow: 'just now',
    minAgo: '{n} min ago',
    hourAgo: '{n} h ago',
    dayAgo: '{n} d ago',
  },
  timeShort: {
    min: '{n}m',
    hour: '{n}h',
    day: '{n}d',
  },

  metrics: {
    band: {
      elite: 'Elite',
      high: 'High',
      medium: 'Medium',
      low: 'Low',
      none: 'No data',
    },
    duration: {
      seconds: '{n}s',
      minutes: '{n} min',
      hours: '{n} h',
      days: '{n} d',
    },
    freq: {
      perDay: '{n}/day',
      perWeek: '{n}/week',
      perMonth: '{n}/month',
    },
  },
}

export default en

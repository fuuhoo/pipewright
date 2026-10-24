/**
 * OAuth 回调原因码 → 前端文案键。
 *
 * 后端在 302 回跳时只带机器码(/login?oauth_error=name_taken),它刻意不含任何配置细节
 * 与 token。把这些码集中翻译一次,登录页与账户页共用同一套口径 —— 否则同一个 name_taken
 * 会在两处翻译成两种说法,用户无法确认自己撞上的是同一件事。
 *
 * 穷举而非 snake→camel 猜测:后端加新码时这里漏改要能被测试抓到,
 * 而自动转换只会静默产出一个不存在的文案键,页面上显示成键名本身。
 */

/** i18n 命名空间(各语言根词条文件里的 `oauthErr`)。 */
export const OAUTH_ERR_NS = 'oauthErr'

/** 已知原因码 → 文案键后缀。取值与 internal/httpapi 的 reason 逐字一致。 */
const REASON_KEYS: Record<string, string> = {
  invalid_state: 'invalidState',
  missing_code: 'missingCode',
  provider_denied: 'providerDenied',
  // 平台在授权页直接拒绝了我们申请的 scope(多为「登录即同步凭据」升过级、而应用没登记该 scope)。
  // 它和「用户点了取消」长得一样但重试永远不会成功,所以后端单独回吐一个码。
  scope_rejected: 'scopeRejected',
  app_not_configured: 'appNotConfigured',
  invalid_provider: 'invalidProvider',
  vault_unavailable: 'vaultUnavailable',
  authorize_failed: 'authorizeFailed',
  exchange_failed: 'exchangeFailed',
  identity_unavailable: 'identityUnavailable',
  domain_not_allowed: 'domainNotAllowed',
  oidc_config_invalid: 'oidcConfigInvalid',
  name_taken: 'nameTaken',
  account_unavailable: 'accountUnavailable',
  role_unavailable: 'roleUnavailable',
  login_disabled: 'loginDisabled',
  already_bound: 'alreadyBound',
  provider_bound: 'providerBound',
  session_unavailable: 'sessionUnavailable',
  login_unavailable: 'loginUnavailable',
  login_failed: 'loginFailed',
  oauth_unavailable: 'oauthUnavailable',
  session_unbound: 'sessionUnbound',
}

/**
 * 把原因码翻成完整文案键。认不出的码回 `oauthErr.generic`,并把它原样带进插值 ——
 * 用户看到「登录未完成(code)」远好于看到一行 `undefined` 或一个键名。
 */
export function oauthErrorKey(reason: string): string {
  const key = REASON_KEYS[reason]
  return key ? `${OAUTH_ERR_NS}.${key}` : `${OAUTH_ERR_NS}.generic`
}

/** 未知原因码时是否要把原文塞进插值(已知码都有人读文案,不需要)。 */
export function oauthErrorShowsRawCode(reason: string): boolean {
  return !REASON_KEYS[reason]
}

/** 后端会回吐的全部原因码(测试用来自省穷举,以及页面上做兜底翻译)。 */
export function knownOAuthErrorReasons(): string[] {
  return Object.keys(REASON_KEYS)
}

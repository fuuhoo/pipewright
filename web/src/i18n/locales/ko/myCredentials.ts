/**
 * v6.2 词条(占位译文,待人工审阅)。键集与 zh-CN 一致。
 * 缺少译文时 i18n 以 fallbackLocale=zh-CN 回退,不会出现裸 key。
 */
export default {
  title: 'My credentials',
  desc: '{user}\'s personal credentials. Personal credentials are visible and usable by you only; global ones are managed by admins. Cipher-level operations belong in the Credential vault.',
  manage: 'Manage my credentials',
  empty: 'You have no credential yet.',
  never: 'Never used',
  colName: 'Name',
  colType: 'Type',
  colScope: 'Scope',
  colMasked: 'Mask',
  colLastUsed: 'Last used',
  scopeGlobal: 'Global',
  scopePersonal: 'Personal',
  vaultUnconfigured: 'Master key not configured — the credential vault is unavailable. Set PIPEWRIGHT_MASTER_KEY and restart.',
  errLoad: 'Failed to load credentials',
  errConn: 'Cannot reach the server',
}

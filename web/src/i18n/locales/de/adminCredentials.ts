/**
 * v6.2 词条(占位译文,待人工审阅)。键集与 zh-CN 一致。
 * 缺少译文时 i18n 以 fallbackLocale=zh-CN 回退,不会出现裸 key。
 */
export default {
  title: 'Global credentials',
  desc: 'View credential metadata for everyone, and disable abusive personal credentials. Cipher-level operations (create / rotate / delete / reveal) belong in the Credential vault.',
  openVault: 'Open credential vault',
  empty: 'No credential yet.',
  never: 'Never used',
  colName: 'Name',
  colType: 'Type',
  colScope: 'Scope',
  colMasked: 'Mask',
  colLastUsed: 'Last used',
  colCreated: 'Created',
  colActions: 'Actions',
  scopeGlobal: 'Global',
  scopePersonal: 'Personal',
  globalNotDisableable: 'Global credentials cannot be disabled',
  disable: 'Disable',
  disableOk: 'Disabled "{name}"',
  vaultUnconfigured: 'Master key not configured — the credential vault is unavailable. Set PIPEWRIGHT_MASTER_KEY and restart.',
  errLoad: 'Failed to load credentials',
  errConn: 'Cannot reach the server',
  errDisable: 'Disable failed',
}

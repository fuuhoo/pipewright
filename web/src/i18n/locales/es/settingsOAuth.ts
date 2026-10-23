export default {
  title: 'Aplicaciones OAuth',
  sectionDesc:
    'Registra una aplicación OAuth (Client ID / Secret) para cada plataforma git para que los usuarios puedan «Conectar» su cuenta con un clic desde la bóveda de credenciales y obtener un token automáticamente, sin pegar manualmente un PAT. El secreto se enmascara al guardarlo y no puede leerse de nuevo tras escribirlo. La misma plataforma también puede registrarse para un segundo uso: que los visitantes entren en esta instancia con su cuenta de la plataforma. DingTalk, Feishu, WeCom y OIDC genérico solo existen para ese segundo uso: nunca entran en la bóveda de credenciales.',
  tabsAria: 'Proveedores de OAuth',
  retry: 'Reintentar',

  providerCustomLabel: 'Autoalojado',
  providerGiteeDesc: 'Gitee',
  providerCustomDesc: 'GitLab / Gitea autoalojado, etc.',

  statusEnabled: 'Activado',
  statusConfiguredIdle: 'Configurado · No activado',
  statusUnconfigured: 'Sin configurar',

  clientIdPlaceholder: 'Obténlo en la página de la aplicación OAuth de la plataforma git',
  clientIdPlaceholderOidc: 'Se emite al registrar un cliente OIDC en tu proveedor de identidad',
  secretOptional: '(tras escribirlo solo se muestra la máscara)',
  secretPlaceholderKeep: 'Déjalo en blanco para conservar el secreto existente',
  secretPlaceholderNew: 'Pega el Client Secret…',
  secretStored: 'Guardado: {masked}',

  toggleAria: 'Activar OAuth de {provider}',
  toggleTitle: 'Activar conexión',
  toggleDesc: 'Una vez activado, el botón «Conectar» de esta plataforma aparece en la bóveda de credenciales',

  lastSaved: 'Último guardado {time}',
  saveBtn: 'Guardar',

  errNoServer: 'No se puede conectar al servidor. Comprueba que el backend esté en ejecución y reintenta',
  errVaultUnconfigured: 'La bóveda no tiene una master key configurada. Establece la variable de entorno PIPEWRIGHT_MASTER_KEY',
  errLoadStatus: 'Error al cargar ({status})',
  errLoadGeneric: 'No se pudieron cargar las aplicaciones OAuth. Inténtalo de nuevo más tarde',

  errClientIdRequiredEnabled: 'El Client ID es obligatorio al activar',
  errSecretRequiredEnabled: 'Se requiere un Client Secret al activar',
  errBaseUrlRequiredCustom: 'Una instancia autoalojada requiere una Base URL',
  errClientIdRequired: 'Introduce el Client ID',
  errBaseUrlRequired: 'Introduce la Base URL',

  toastSaveFailed: 'Error al guardar',
  toastSaved: 'Aplicación OAuth guardada',
  errNoServerShort: 'No se puede conectar al servidor',
  unknownError: 'Error desconocido',

  justNow: 'ahora mismo',
  minutesAgo: 'hace {n} minutos',
  hoursAgo: 'hace {n} horas',
  daysAgo: 'hace {n} días',

  // ─── cambio de uso: dos configuraciones por plataforma (credenciales / inicio de sesión) ───
  // Dos configuraciones, dos URLs de callback: una aplicación OAuth suele admitir una
  // sola URL, compartirla entre los dos usos bloquearía al otro.
  purposeSwitchAria: 'Finalidad de OAuth',
  purposeVault: 'Credenciales del repositorio',
  purposeVaultHint: 'El usuario conectado autoriza una vez y el token git queda en la bóveda',
  purposeLogin: 'Inicio de sesión de plataforma',
  purposeLoginHint: 'Los visitantes sin sesión entran con su cuenta de la plataforma; las cuentas creadas automáticamente reciben el rol mínimo',
  purposeVaultNote:
    'Esta configuración solo gobierna el botón «Conectar cuenta git». Lo que debes registrar en la plataforma es la Callback URL de abajo; si cambia, actualiza también esa aplicación.',
  purposeLoginNote:
    'Al activarlo, este proveedor aparece en la página de inicio de sesión. Cada uso necesita su propia aplicación OAuth: una aplicación solo admite una URL de callback.',
  callbackHint: 'Pega esta URL en el campo «callback / Redirect URI» de la aplicación OAuth de tu plataforma git',
  callbackHintOidc: 'Pega esta URL en el campo «callback / Redirect URI» del cliente OIDC de tu proveedor de identidad',
  callbackCopy: 'Haz clic para seleccionarlo todo y cópialo a la plataforma',
  domainsLabel: 'Allowlist de dominios de correo',
  domainsOptional: '(vacío = cualquier dominio)',
  domainsPlaceholder: 'corp.example.com, hr.example.com',
  domainsHint:
    'Solo pueden entrar las cuentas cuyo correo esté en alguno de estos dominios. Con la lista vacía cualquier correo obtiene una cuenta creada automáticamente (siempre con el rol mínimo).',
  errDomainsInvalid:
    'La allowlist de correos contiene un elemento no válido: escribe solo el dominio (p. ej. corp.example.com), sin protocolo, ruta ni espacios',
  toggleTitleLogin: 'Activar inicio de sesión',
  toggleDescLogin: 'Al activarlo, la página de inicio de sesión muestra un botón «Entrar con esta plataforma»',
  // ─── Sincronizar credenciales al iniciar sesión (solo las 4 plataformas git)
  // Aquí se decide cuánto se lleva un solo inicio de sesión: por eso está
  // apagado, y el texto dice a la vez el alcance ganado y cómo recuperarse.
  syncTitle: 'Sincronizar una credencial git al iniciar sesión',
  syncDesc: 'Tras iniciar sesión con esta plataforma, guarda el token de acceso de esta autorización como credencial git personal de esta cuenta',
  syncAria: 'Activar la sincronización al iniciar sesión para {provider}',
  syncNote:
    'Al activarlo, el inicio de sesión pide más permisos: de «solo identidad» a «leer repositorios». Antes de que venza, la plataforma renueva el token; si la renovación se rechaza, la credencial queda marcada como «autorización revocada» y volver a iniciar sesión la recupera. La credencial sincronizada solo es visible para esta cuenta — qué credencial usa un proyecto lo sigues eligiendo tú.',
  errSyncUnsupported:
    'Esta fuente de identidad no puede dar un token que clone un repositorio: la sincronización al iniciar sesión solo está abierta a plataformas git (Gitee / GitHub / GitLab / autoalojada)',

  // ─── Proveedor de identidad OIDC (solo aparece bajo «Inicio de sesión») ─────
  // El backend resuelve los endpoints al guardar: con una mala configuración el
  // fallo se vería como «la pantalla de consentimiento fue bien y al volver rompió»,
  // así que es mejor que falle en el guardado.
  providerOidcDesc: 'OIDC / SSO genérico (Keycloak, Authentik…)',
  oidcDiscoveryLabel: 'URL de Discovery',
  oidcDiscoveryOptional: '(esta o los tres endpoints de abajo)',
  oidcDiscoveryPlaceholder: 'https://sso.example.com/realms/my/.well-known/openid-configuration',
  oidcDiscoveryHint:
    'Si la rellenas, los endpoints de autorización / token / userinfo salen del documento; al guardar se consulta de verdad y falla si no es accesible.',
  oidcOverrideOptional: '(vacío = usa el documento)',
  oidcAuthorizeLabel: 'Authorization Endpoint',
  oidcTokenLabel: 'Token Endpoint',
  oidcUserinfoLabel: 'Userinfo Endpoint',
  oidcEndpointPlaceholder: 'https://sso.example.com/…',
  oidcEndpointsHint:
    'Si el documento no es accesible (red aislada, un gateway lo bloquea), rellena los tres a mano; lo escrito a mano gana al documento.',
  oidcIdClaimLabel: 'Claim identificador',
  oidcIdClaimOptional: '(vacío = sub)',
  oidcIdClaimHint:
    'Qué claim identifica la cuenta de forma única. Por defecto sub, que la especificación exige inmutable. Usar el correo o el nombre de usuario hace que un cambio futuro acabe en la cuenta de otra persona.',
  oidcScopesLabel: 'Scopes',
  oidcScopesOptional: '(vacío = openid profile email)',
  oidcScopesHint:
    'Separados por espacios; openid es obligatorio. Si no llega un correo, la allowlist de dominios bloquea el acceso.',
  oidcUserinfoNote:
    'La identidad se lee del endpoint userinfo: esta versión no valida la firma ni el audience de id_token, así que conecta solo un proveedor interno o por HTTPS.',
  errOidcEndpointsIncomplete:
    'Para activar OIDC indica una URL de Discovery o rellena los tres endpoints de autorización / token / userinfo',
  errOidcConfigInvalid:
    'No se han podido resolver los endpoints: URL incorrecta, discovery inaccesible o los tres endpoints están incompletos (la configuración guardada no se ha tocado)',

  // ─── DingTalk / Feishu / WeCom (solo bajo «Inicio de sesión de plataforma») ─
  // Los endpoints oficiales van quemados en el preajuste del backend; la página
  // solo ofrece sustituciones, para un proxy interno o un despliegue privado.
  providerDingTalkDesc: 'Inicio de sesión por código QR de DingTalk',
  providerFeishuDesc: 'Inicio de sesión por código QR de Feishu',
  providerWecomDesc: 'Inicio de sesión por código QR de WeCom (app propia)',
  clientIdPlaceholderPlatform:
    'El Client ID de la ficha de la app en la plataforma abierta (en WeCom, el ID de empresa / corpid)',
  callbackHintPlatform:
    'Pega esta URL en el campo «callback / redirect URL» de la app de la plataforma abierta; WeCom además exige registrar el dominio de esta instancia como dominio de confianza.',
  oidcScopesPlaceholder: 'openid profile email',
  platformOverrideOptional: '(vacío = la URL oficial de la plataforma)',
  platformEndpointPlaceholder: 'https://… (solo para proxy interno o despliegue privado)',
  platformEndpointsHint:
    'Las direcciones de autorización / canje / identidad ya traen los valores oficiales y suelen dejarse vacías; cúbrelas solo para un proxy interno o un despliegue privado, y el valor manual tiene prioridad.',
  agentIdPlaceholder: 'El AgentId de los detalles de la app de WeCom, p. ej. 1000002',
  agentIdHint:
    'La URL de escaneo lo necesita (el parámetro se llama agentid) para resolver la identidad del miembro; en el campo Client ID va el ID de la empresa (corpid), no este número.',
  platformScopesOptional: '(vacío = el predeterminado)',
  platformScopesPlaceholder: 'separados por espacio, p. ej. contact:user.base:readonly',
  platformScopesHint:
    'DingTalk usa openid por defecto; Feishu no solicita ningún scope por defecto, porque pedir un permiso que la app no tiene se rechaza de inmediato. Para filtrar con la lista de dominios de correo, la plataforma debe devolver un correo.',
  platformIdentityNote:
    'Las cuentas se emparejan solo con el identificador estable que emite la plataforma (DingTalk / Feishu usan unionId y, si no, openId; WeCom usa el ID de empresa más el userid del miembro). El apodo y el correo nunca identifican una cuenta, así que cambiar de nombre no puede acabar en la cuenta de otra persona.',
  errAgentIdRequired: 'WeCom necesita un AgentId (en los detalles de la app)',
  errPlatformConfigInvalid:
    'Las URLs de sustitución deben ser http(s) y sin credenciales incrustadas; déjalas vacías para usar la dirección oficial (la configuración guardada no se ha tocado)',
}

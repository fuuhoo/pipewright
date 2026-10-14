export default {
  title: 'Aplicaciones OAuth',
  sectionDesc:
    'Registra una aplicación OAuth (Client ID / Secret) para cada plataforma git para que los usuarios puedan «Conectar» su cuenta con un clic desde la bóveda de credenciales y obtener un token automáticamente, sin pegar manualmente un PAT. El secreto se enmascara al guardarlo y no puede leerse de nuevo tras escribirlo. La misma plataforma también puede registrarse para un segundo uso: que los visitantes entren en esta instancia con su cuenta de la plataforma.',
  tabsAria: 'Proveedores de OAuth',
  retry: 'Reintentar',

  providerCustomLabel: 'Autoalojado',
  providerGiteeDesc: 'Gitee',
  providerCustomDesc: 'GitLab / Gitea autoalojado, etc.',

  statusEnabled: 'Activado',
  statusConfiguredIdle: 'Configurado · No activado',
  statusUnconfigured: 'Sin configurar',

  clientIdPlaceholder: 'Obténlo en la página de la aplicación OAuth de la plataforma git',
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
  callbackCopy: 'Haz clic para seleccionarlo todo y cópialo a la plataforma',
  domainsLabel: 'Allowlist de dominios de correo',
  domainsOptional: '(vacío = cualquier dominio)',
  domainsPlaceholder: 'corp.example.com, hr.example.com',
  domainsHint:
    'Solo pueden entrar las cuentas cuyo correo esté en alguno de estos dominios. Con la lista vacía cualquier correo obtiene una cuenta automática (siempre con el rol mínimo).',
  errDomainsInvalid:
    'La allowlist de correos contiene un elemento no válido: escribe solo el dominio (p. ej. corp.example.com), sin protocolo, ruta ni espacios',
  toggleTitleLogin: 'Activar inicio de sesión',
  toggleDescLogin: 'Al activarlo, la página de inicio de sesión muestra un botón «Entrar con esta plataforma»',
}

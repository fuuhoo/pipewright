export default {
  title: 'Endpoints MCP',
  desc: 'Puntos de acceso para clientes máquina: un cliente de IA crea proyectos, configura pipelines y lanza builds vía MCP, o un script llama a la API de la plataforma directamente. Un endpoint = su propia URL + un token + dos restricciones (qué herramientas puede usar, qué grupos puede ver). Actúa en nombre de una cuenta concreta, con exactamente los permisos de esa persona al iniciar sesión. La plataforma solo guarda un resumen: el texto en plano se entrega una vez, al crear el endpoint, y nunca vuelve a leerse.',
  issue: 'Nuevo endpoint',
  retry: 'Reintentar',
  listTitle: 'Endpoints',
  tokenCount: '{n}',
  emptyList: 'Aún no hay endpoints MCP. Cree uno y meta su URL y su token en la configuración MCP de su cliente de IA.',
  metaOwner: 'Titular: {owner}',
  metaCreatedBy: 'Emitido por {who}',
  metaCreated: 'Creado el {at}',
  metaLastUsed: 'Último uso {at}',
  metaTools: "Herramientas {scope}",
  metaGroups: "Grupos {scope}",
  scopeToolsAll: "sin límite",
  scopeToolsSome: "solo {n}",
  scopeGroupsAll: "sin límite",
  scopeGroupsSome: "solo {names}",
  neverUsed: 'Nunca usado',
  revoke: 'Revocar',
  revokeTitle: 'Revocar endpoint',
  revokeConfirm:
    '¿Revoca el endpoint {name}? Revocar borra la fila: la siguiente petición que lo lleve recibe un 401 directo y esta URL deja de funcionar. No tiene retorno.',
  confirmRevoke: 'Confirmar revocación',
  revoking: 'Revocando…',
  cancel: 'Cancelar',
  done: 'Ya lo guardé, cerrar',
  copy: 'Copiar',
  copied: 'Copiado',
  modalTitle: 'Nuevo endpoint MCP',
  issuing: 'Creando…',
  fieldName: 'Nombre del endpoint',
  nameHint: 'Nombre para personas: indica de qué máquina o cliente se trata (máximo {max} caracteres).',
  fieldOwner: 'Titular',
  ownerSelf: 'Yo mismo',
  ownerHint:
    'El token trabaja como esta cuenta — esta es la columna que se consulta después para saber «quién lanzó compilaciones con él». Si dejas «Yo mismo», se emite para la persona conectada ahora.',
  fieldTools: "Herramientas permitidas",
  preset: {
    all: "Todas las herramientas",
    readonly: "Solo lectura",
    ops: "Solo lectura + ejecutar"
  },
  presetCustom: "Selección manual",
  tagReadOnly: "solo lectura",
  toolsHintAll: "No marcar ninguna = sin límite: siguen disponibles todas las herramientas MCP de la plataforma.",
  toolsHintSome: "Solo se exponen las {n} de {total} marcadas: tools/list del cliente no muestra ninguna otra y una llamada fuera del alcance se rechaza al instante.",
  fieldGroups: "Grupos visibles",
  groupProjectCount: "{n} proyectos",
  noGroups: 'Todavía no hay grupos. Cree primero uno en «Grupos y permisos»; si limita los grupos sin ninguno, el endpoint no podrá ver nada.',
  groupsHintAll: "No marcar ninguna = sin límite: el token ve todo lo que ya puede ver su titular.",
  groupsHintSome: "Solo se expone «grupos marcados ∩ lo que el titular ya puede ver». Ojo: al limitar grupos quedan fuera los proyectos sin grupo, incluso si el titular es administrador.",
  plaintextLabel: 'Token en claro',
  issuedTitle: 'Endpoint creado',
  issuedOnce:
    'El texto en claro se muestra solo esta vez: en la base de datos queda únicamente el resumen y, al cerrar este cuadro, la plataforma no podrá volver a mostrarlo. Copiándolo antes de cerrar — si lo pierdes, solo queda revocarlo y emitir otro.',
  scopeEcho: 'Alcance de este endpoint: herramientas {tools} · grupos {groups}',
  endpointLabel: "Dirección del endpoint MCP",
  howToTitle: "Después conéctalo al cliente de IA: elige la pestaña de tu cliente y copia este bloque:",
  client: {
    cursor: "Cursor",
    vscode: "VS Code",
    claudeCode: "Claude Code",
    claudeDesktop: "Claude Desktop",
    generic: "Otros clientes"
  },
  snippetWhere: {
    cursor: "Pégalo en el mcp.json de Cursor (o Ajustes → MCP Servers → Add)",
    vscode: "Pégalo en el .vscode/mcp.json del proyecto — VS Code usa la clave raíz servers, no mcpServers",
    claudeCode: "Pégalo en un .mcp.json en la raíz del proyecto",
    claudeCodeCli: "O añádelo con un comando (mismo resultado):",
    claudeDesktop: "La configuración de Claude Desktop solo acepta stdio local: pégalo en claude_desktop_config.json; necesita el binario pipewright en esta máquina",
    generic: "Forma genérica mcpServers: para otros clientes con HTTP remoto, ajuste los nombres de clave"
  },
  snippetHint: 'Reinicie el cliente después. Ambas formas exponen las mismas herramientas con el mismo token: la de stdio levanta un proceso local que sale hacia la plataforma (útil cuando la plataforma está tras un NAT y la máquina no abre puertos); la de HTTP se conecta a la URL propia de este endpoint. Mantenga el token en env y no en argumentos de línea de comandos: cualquiera en la misma máquina puede leer comandos con ps.',
  errNameEmpty: 'Introduzca un nombre de endpoint',
  errNameTooLong: 'El nombre del endpoint es demasiado largo (máximo {max} caracteres)',
  errLoadConn: 'No se puede conectar con el servicio; revisa la red y reintenta',
  errLoadStatus: 'Error al cargar los endpoints (HTTP {status})',
  errLoadRetry: 'Error al cargar los endpoints; inténtalo de nuevo',
  errConnRetry: 'No se puede conectar con el servicio; revisa la red y reintenta',
  errIssueStatus: 'Error al crear el endpoint (HTTP {status})',
  errIssueRetry: 'Error al crear el endpoint. Inténtelo de nuevo.',
  errRevokeStatus: 'Error al revocar el endpoint (HTTP {status})',
  errRevokeRetry: 'Error al revocar el endpoint. Inténtelo de nuevo.',
  editScope: 'Editar alcance',
  editTitle: 'Editar alcance del endpoint',
  editWho:
    'Cambiar el alcance no toca el token: la URL y el texto en plano siguen igual, así que ningún cliente necesita reconfigurarse.',
  editToolsHintAll:
    'Nada marcado = vuelve a sin límite: al guardar, este endpoint expone todas las herramientas MCP de la plataforma.',
  editGroupsHintAll:
    'Nada marcado = vuelve a sin límite: al guardar, este endpoint ve todo lo que ya puede ver su propietario.',
  editEffectHint:
    'Guardar surte efecto de inmediato: la siguiente llamada del cliente sigue el nuevo alcance, sin reiniciar ni cambiar el token.',
  save: 'Guardar alcance',
  saving: 'Guardando…',
  optionGone: 'ya no existe',
  endpointOwnHint: 'Esta URL solo acepta el token con el que se creó; cualquier otro recibe un 403.',
  errEditStatus: 'Error al guardar el alcance (HTTP {status})',
  errEditRetry: 'Error al guardar el alcance. Inténtelo de nuevo.',
  guide: {
    cta: 'Guía de uso',
    open: 'Ver la guía completa',
    title: 'Endpoint MCP de Pipewright — guía de uso',
    hint: 'Copie todo el bloque y páselo a un cliente de IA o a un compañero: la URL, el token, las herramientas permitidas y las trampas ya están dentro.',
    tokenMissing:
      'El token en texto claro solo se muestra al crear el endpoint, así que el texto lleva el marcador {placeholder} — sustitúyalo por su token antes de pegarlo en ningún sitio.',
    tokenPlaceholder: '<pegue aquí el token>',
    intro:
      'Este es un endpoint MCP de Pipewright, una plataforma de CI/CD autoalojada que cubre proyectos, pipelines, compilaciones y despliegues. Con este endpoint puede listar proyectos, configurar un pipeline, lanzar una compilación, consultar el estado y los registros de una ejecución y cancelarla. El token actúa como {owner}, con los mismos permisos que esa persona tiene al iniciar sesión, y este endpoint además reduce el alcance (vea "Alcance permitido").',
    connectTitle: 'Cómo conectarse',
    httpNote:
      'Opción 1 — HTTP directo (recomendada). Integre este fragmento en la configuración MCP de su cliente: el mcp.json de Cursor y el .mcp.json de Claude Code usan la clave raíz mcpServers, mientras que el .vscode/mcp.json de VS Code usa servers:',
    stdioNote:
      'Opción 2 — puente stdio local. Úselo cuando el binario pipewright esté instalado en esta máquina y la plataforma viva en una intranet que no quiere exponer:',
    rawNote:
      'Opción 3 — JSON-RPC en crudo (scripts y depuración). Haga POST a la URL del endpoint con Authorization: Bearer más el token, Content-Type: application/json y Accept: application/json, text/event-stream; llame primero a initialize, luego tools/list y después tools/call. Ejemplo:',
    scopeTitle: 'Alcance permitido',
    toolsAll: 'Herramientas: las {n} están habilitadas.',
    toolsSome: 'Herramientas: solo {n} de {total} están habilitadas.',
    groupsAll: 'Grupos: sin límite, se ve todo lo que el propietario ya puede ver.',
    groupsSome:
      'Grupos: solo estos — {names}. Los proyectos sin grupo quedan fuera, aunque el propietario sea administrador.',
    scopeNote:
      'Los dos alcances solo intersectan y solo se estrechan: las herramientas fuera de la lista ni aparecen en tools/list, y llamarlas se rechaza en el momento; no es un error de configuración del cliente.',
    rulesTitle: 'Convenciones y trampas',
    ruleOnce:
      'El token en texto claro aparece una sola vez, al crearlo; la plataforma guarda solo el resumen y no lo devuelve jamás. Si lo perdió, revoque y emita uno nuevo en vez de buscarlo en repositorios o tickets.',
    ruleBaseURL:
      'En la forma stdio, PIPEWRIGHT_URL debe ser la raíz del sitio, no la URL de este endpoint: las herramientas vuelven a llamar a la API REST añadiendo /api/..., y con la URL del endpoint la ruta queda rota.',
    ruleWrite:
      'trigger_run, cancel_run, create_project y set_pipeline cambian de verdad el estado de la plataforma; confirme antes con el usuario el proyecto, la rama y el impacto.',
    ruleLogs:
      'Los registros de compilación pueden ser enormes: lea la cola con get_run_logs en lugar de meter toda la salida en el contexto.',
    rulePlain:
      'Si la URL de la plataforma es http, el token viaja en claro; use https para lo público o deje el endpoint dentro de una red confiable.',
    tagWrite: 'escritura',
  },
}

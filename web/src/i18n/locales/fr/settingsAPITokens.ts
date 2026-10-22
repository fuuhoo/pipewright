export default {
  title: 'Endpoints MCP',
  desc: "Points d'accès pour clients machine : un client IA crée des projets, configure des pipelines et lance des builds via MCP, ou un script appelle directement l'API de la plateforme. Un endpoint = sa propre URL + un jeton + deux filtres (les outils autorisés, les groupes visibles). Il agit pour le compte d'un compte précis, avec exactement les permissions de cette personne connectée. La plateforme ne stocke qu'un condensé : la valeur en clair n'est montrée qu'une fois, à la création, et ne se relit jamais.",
  issue: 'Nouvel endpoint',
  retry: 'Réessayer',
  listTitle: 'Endpoints',
  tokenCount: '{n}',
  emptyList: "Aucun endpoint MCP pour l'instant. En créez un, puis mettez son URL et son jeton dans la configuration MCP de votre client IA.",
  metaOwner: 'Titulaire {owner}',
  metaCreatedBy: 'Émis par {who}',
  metaCreated: 'Créé le {at}',
  metaLastUsed: 'Dernière utilisation {at}',
  metaTools: "Outils {scope}",
  metaGroups: "Groupes {scope}",
  scopeToolsAll: "sans limite",
  scopeToolsSome: "{n} seulement",
  scopeGroupsAll: "sans limite",
  scopeGroupsSome: "{names} seulement",
  neverUsed: 'Jamais utilisé',
  revoke: 'Révoquer',
  revokeTitle: "Révoquer l'endpoint",
  revokeConfirm:
    "Révoquer l'endpoint {name} ? Révoquer supprime la ligne : la prochaine requête portant ce jeton reçoit un 401 sec et cette URL cesse de fonctionner. C'est irréversible.",
  confirmRevoke: 'Confirmer la révocation',
  revoking: 'Révocation…',
  cancel: 'Annuler',
  done: "Je l'ai enregistré, fermer",
  copy: 'Copier',
  copied: 'Copié',
  modalTitle: 'Nouvel endpoint MCP',
  issuing: 'Création…',
  fieldName: 'Nom de l’endpoint',
  nameHint: 'Nom lisible par des humains : indique la machine ou le client concerné ({max} caractères maximum).',
  fieldOwner: 'Titulaire',
  ownerSelf: 'Moi-même',
  ownerHint:
    "Le jeton travaille au nom de ce compte — c'est la colonne qu'on consulte après coup pour savoir « qui a lancé des builds avec lui ». Laisser « Moi-même » l'émet pour la personne connectée.",
  fieldTools: "Outils autorisés",
  preset: {
    all: "Tous les outils",
    readonly: "Lecture seule",
    ops: "Lecture seule + exécution"
  },
  presetCustom: "Sélection manuelle",
  tagReadOnly: "lecture seule",
  toolsHintAll: "Rien coché = aucune limite : tous les outils MCP de la plateforme restent disponibles.",
  toolsHintSome: "Seuls les {n} sur {total} outils cochés sont exposés : tools/list du client n’en montre pas d’autres, et un appel hors périmètre est refusé immédiatement.",
  fieldGroups: "Groupes visibles",
  groupProjectCount: "{n} projets",
  noGroups: "Aucun groupe pour l'instant. Créez-en un dans « Groupes et permissions » d'abord : limiter les groupes sans groupe aboutit à un endpoint qui ne voit rien.",
  groupsHintAll: "Rien coché = aucune limite : le jeton voit tout ce que son titulaire voit déjà.",
  groupsHintSome: "On n’expose que « groupes cochés ∩ ce que le titulaire voit déjà ». Attention : limiter les groupes masque les projets sans groupe, même si le titulaire est administrateur.",
  plaintextLabel: 'Jeton en clair',
  issuedTitle: 'Endpoint créé',
  issuedOnce:
    "Le texte en clair ne s'affiche que cette fois : la base ne contient que l'empreinte et, une fois cette fenêtre fermée, la plateforme ne pourra plus le réafficher. Copiez-le avant de fermer — en cas de perte, il faudra révoquer et réémettre.",
  scopeEcho: 'Portée de cet endpoint : outils {tools} · groupes {groups}',
  endpointLabel: "Adresse du point de terminaison MCP",
  howToTitle: "Ensuite, branchez-le sur le client IA — choisissez l'onglet de votre client et copiez ce bloc :",
  client: {
    cursor: "Cursor",
    vscode: "VS Code",
    claudeCode: "Claude Code",
    claudeDesktop: "Claude Desktop",
    generic: "Autres clients"
  },
  snippetWhere: {
    cursor: "Coller dans le mcp.json de Cursor (ou Paramètres → MCP Servers → Add)",
    vscode: "Coller dans le .vscode/mcp.json du projet — VS Code utilise la clé racine servers, pas mcpServers",
    claudeCode: "Coller dans un .mcp.json à la racine du projet",
    claudeCodeCli: "Ou l’ajouter par une commande (même résultat) :",
    claudeDesktop: "La configuration de Claude Desktop n'accepte que du stdio local : coller dans claude_desktop_config.json, en ayant installé le binaire pipewright sur cette machine",
    generic: "Forme mcpServers générique : pour les autres clients en HTTP distant, ajustez les noms de clés"
  },
  snippetHint: "Redémarrez le client ensuite. Les deux formes exposent les mêmes outils avec le même jeton : la forme stdio lance un processus local qui se connecte vers la plateforme (utile quand la plateforme est derrière une NAT et la machine n'ouvre aucun port) ; la forme HTTP se branche sur l'URL propre à cet endpoint. Gardez le jeton dans env plutôt qu'en argument de ligne de commande — n'importe qui sur la même machine lit les lignes de commande via ps.",
  errNameEmpty: "Veuillez saisir un nom d'endpoint",
  errNameTooLong: 'Nom d’endpoint trop long ({max} caractères maximum)',
  errLoadConn: 'Connexion au service impossible ; vérifiez le réseau puis réessayez',
  errLoadStatus: 'Échec du chargement des endpoints (HTTP {status})',
  errLoadRetry: 'Échec du chargement des endpoints, réessayez',
  errConnRetry: 'Connexion au service impossible ; vérifiez le réseau puis réessayez',
  errIssueStatus: "Échec de la création de l'endpoint (HTTP {status})",
  errIssueRetry: 'Échec de la création. Réessayez.',
  errRevokeStatus: "Échec de la révocation de l'endpoint (HTTP {status})",
  errRevokeRetry: 'Échec de la révocation. Réessayez.',
  editScope: 'Modifier la portée',
  editTitle: "Modifier la portée de l'endpoint",
  editWho:
    "Changer la portée ne touche pas le jeton : l'URL et la valeur en clair restent les mêmes, aucun client n'a besoin d'être reconfiguré.",
  editToolsHintAll:
    "Rien de coché = retour à sans limite : à l'enregistrement, cet endpoint expose tous les outils MCP de la plateforme.",
  editGroupsHintAll:
    "Rien de coché = retour à sans limite : à l'enregistrement, cet endpoint voit tout ce que son propriétaire voit déjà.",
  editEffectHint:
    "L'enregistrement prend effet aussitôt : le prochain appel du client suit la nouvelle portée, sans redémarrage ni nouveau jeton.",
  save: 'Enregistrer la portée',
  saving: 'Enregistrement…',
  optionGone: 'a disparu',
  endpointOwnHint:
    "Cette URL n'accepte que le jeton délivré à sa création ; tout autre jeton renvoie un 403.",
  errEditStatus: "Échec de l'enregistrement de la portée (HTTP {status})",
  errEditRetry: "Échec de l'enregistrement de la portée. Réessayez.",
  guide: {
    cta: "Mode d'emploi",
    open: "Voir le bloc complet",
    title: "Point de terminaison MCP Pipewright — mode d'emploi",
    hint: "Copiez tout le bloc et donnez-le à un client IA ou à un collègue : l'URL, le jeton, les outils autorisés et les pièges y sont déjà.",
    tokenMissing:
      "Le jeton en clair n'est affiché qu'à la création du point de terminaison ; le texte ci-dessous porte le marqueur {placeholder} — remplacez-le par votre jeton avant de le coller ailleurs.",
    tokenPlaceholder: "<collez le jeton ici>",
    intro:
      "Voici un point de terminaison MCP de Pipewright, une plateforme CI/CD auto-hébergée qui couvre projets, pipelines, builds et déploiements. Via ce point, vous pouvez lister les projets, configurer un pipeline, lancer un build, consulter l'état et les journaux d'une exécution et l'annuler. Le jeton agit au nom de {owner}, avec les mêmes permissions que cette personne connectée, et ce point restreint encore ce périmètre (voir « Périmètre autorisé »).",
    connectTitle: "Comment se connecter",
    httpNote:
      "Option 1 — HTTP direct (recommandé). Intégrez ce bloc dans la configuration MCP du client : mcp.json de Cursor et .mcp.json de Claude Code utilisent la racine mcpServers, tandis que .vscode/mcp.json de VS Code utilise servers :",
    stdioNote:
      "Option 2 — pont stdio local. À utiliser quand le binaire pipewright est installé sur cette machine et que la plateforme reste en réseau interne sans exposition :",
    rawNote:
      "Option 3 — JSON-RPC brut (scripts et débogage). Faites un POST sur l'URL du point avec Authorization: Bearer et le jeton, Content-Type: application/json et Accept: application/json, text/event-stream ; appelez d'abord initialize, puis tools/list, ensuite tools/call. Exemple :",
    scopeTitle: "Périmètre autorisé",
    toolsAll: "Outils : les {n} sont exposés.",
    toolsSome: "Outils : seuls {n} sur {total} sont exposés.",
    groupsAll: "Groupes : aucune limite, tout ce que le propriétaire voit déjà reste visible.",
    groupsSome:
      "Groupes : seuls ceux-ci sont visibles — {names}. Les projets sans groupe restent hors champ, même si le propriétaire est administrateur.",
    scopeNote:
      "Les deux périmètres ne font que s'intersecter et ne peuvent que se rétrécir : un outil hors liste n'apparaît même pas dans tools/list, et l'appeler est refusé sur le champ ; ce n'est pas une erreur de configuration du client.",
    rulesTitle: "Conventions et pièges",
    ruleOnce:
      "Le jeton en clair n'apparaît qu'une fois, à la création ; la plateforme ne garde que l'empreinte et ne le rendra jamais. En cas de perte, révoquez et émettez-en un nouveau au lieu de fouiller les dépôts et les tickets.",
    ruleBaseURL:
      "En mode stdio, PIPEWRIGHT_URL doit être la racine du site, et non l'URL de ce point : les outils rappellent l'API REST en ajoutant /api/..., et l'URL du point casse ce chemin.",
    ruleWrite:
      "trigger_run, cancel_run, create_project et set_pipeline modifient vraiment l'état de la plateforme ; confirmez d'abord le projet, la branche et l'impact avec l'utilisateur.",
    ruleLogs:
      "Les journaux de build peuvent être énormes : lisez la fin avec get_run_logs au lieu de faire entrer toute la sortie dans le contexte.",
    rulePlain:
      "Si l'URL de la plateforme est en http, le jeton circule en clair ; utilisez https pour le public, ou gardez le point dans un réseau de confiance.",
    tagWrite: "écriture",
  },
}

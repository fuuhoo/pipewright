export default {
  title: 'Applications OAuth',
  sectionDesc:
    'Enregistrez une application OAuth (Client ID / Secret) pour chaque plateforme git afin que les utilisateurs puissent « Connecter » leur compte en un clic depuis le coffre-fort d’identifiants et obtenir automatiquement un jeton, sans coller manuellement un PAT. Le secret est masqué dès son enregistrement et ne peut plus être lu après son écriture. La même plateforme peut aussi servir un second usage : laisser les visiteurs se connecter à cette instance avec leur compte plateforme. DingTalk, Feishu, WeCom et OIDC générique n’existent que pour ce second usage — ils n’entrent jamais dans le coffre-fort d’identifiants.',
  tabsAria: 'Fournisseurs OAuth',
  retry: 'Réessayer',

  providerCustomLabel: 'Auto-hébergé',
  providerGiteeDesc: 'Gitee',
  providerCustomDesc: 'GitLab / Gitea auto-hébergé, etc.',

  statusEnabled: 'Activé',
  statusConfiguredIdle: 'Configuré · Non activé',
  statusUnconfigured: 'Non configuré',

  clientIdPlaceholder: 'Obtenez-le sur la page de l’application OAuth de la plateforme git',
  clientIdPlaceholderOidc: 'Délivré lorsque vous déclarez un client OIDC chez votre fournisseur d’identité',
  secretOptional: '(seul le masque est affiché après écriture)',
  secretPlaceholderKeep: 'Laissez vide pour conserver le secret existant',
  secretPlaceholderNew: 'Collez le Client Secret…',
  secretStored: 'Enregistré : {masked}',

  toggleAria: 'Activer OAuth {provider}',
  toggleTitle: 'Activer la connexion',
  toggleDesc: 'Une fois activé, le bouton « Connecter » de cette plateforme apparaît dans le coffre-fort d’identifiants',

  lastSaved: 'Dernier enregistrement {time}',
  saveBtn: 'Enregistrer',

  errNoServer: 'Impossible de joindre le serveur. Vérifiez que le backend est en cours d’exécution puis réessayez',
  errVaultUnconfigured: 'Le coffre-fort n’a pas de master key configurée. Définissez la variable d’environnement PIPEWRIGHT_MASTER_KEY',
  errLoadStatus: 'Échec du chargement ({status})',
  errLoadGeneric: 'Échec du chargement des applications OAuth. Veuillez réessayer plus tard',

  errClientIdRequiredEnabled: 'Le Client ID est requis lorsque c’est activé',
  errSecretRequiredEnabled: 'Un Client Secret est requis lorsque c’est activé',
  errBaseUrlRequiredCustom: 'Une instance auto-hébergée nécessite une Base URL',
  errClientIdRequired: 'Veuillez saisir le Client ID',
  errBaseUrlRequired: 'Veuillez saisir la Base URL',

  toastSaveFailed: 'Échec de l’enregistrement',
  toastSaved: 'Application OAuth enregistrée',
  errNoServerShort: 'Impossible de joindre le serveur',
  unknownError: 'Erreur inconnue',

  justNow: 'à l’instant',
  minutesAgo: 'il y a {n} minutes',
  hoursAgo: 'il y a {n} heures',
  daysAgo: 'il y a {n} jours',

  // ─── bascule de finalité : deux configurations par plateforme (identifiants / connexion) ───
  // Deux configurations, deux URL de callback : une application OAuth n’en enregistre
  // habituellement qu’une, la partager entre les deux finalités les bloquerait.
  purposeSwitchAria: 'Finalité OAuth',
  purposeVault: 'Identifiants de dépôt',
  purposeVaultHint: 'L’utilisateur connecté récupère un jeton git dans le coffre-fort après autorisation',
  purposeLogin: 'Connexion plateforme',
  purposeLoginHint: 'Les visiteurs sans session entrent avec leur compte plateforme ; les comptes créés automatiquement ne reçoivent que le rôle minimal',
  purposeVaultNote:
    'Cette configuration ne pilote que le bouton « Connecter le compte git ». Ce qu’il faut déclarer côté plateforme, c’est l’URL de callback ci-dessous ; si elle change, mettez aussi l’application à jour.',
  purposeLoginNote:
    'Une fois activé, ce fournisseur apparaît sur la page de connexion. Chaque finalité exige sa propre application OAuth : une application n’accepte qu’une seule URL de callback.',
  callbackHint: 'Saisissez cette URL dans le champ « callback / Redirect URI » de l’application OAuth de votre plateforme git',
  callbackHintOidc: 'Saisissez cette URL dans le champ « callback / Redirect URI » du client OIDC de votre fournisseur d’identité',
  callbackCopy: 'Cliquez pour tout sélectionner, puis copiez-le côté plateforme',
  domainsLabel: 'Allowlist de domaines d’e-mail',
  domainsOptional: '(vide = tous les domaines)',
  domainsPlaceholder: 'corp.example.com, hr.example.com',
  domainsHint:
    'Seuls les comptes dont l’e-mail relève d’un de ces domaines peuvent se connecter. Allowlist vide : n’importe quel e-mail obtient un compte automatique (toujours avec le rôle minimal).',
  errDomainsInvalid:
    'L’allowlist de domaines contient un élément invalide : indiquez le domaine seul (ex. corp.example.com), sans protocole, chemin ni espace',
  toggleTitleLogin: 'Activer la connexion',
  toggleDescLogin: 'Une fois activé, la page de connexion affiche un bouton « Se connecter avec cette plateforme »',

  // ─── Forme git (auto-hébergée : forme des endpoints / OIDC : déclarer que c’est
  // aussi une plateforme git). Le même champ ne dit pas la même chose selon la carte.
  // En auto-hébergé, il décide quelle URL d’autorisation est construite — se tromper
  // mène à un 404 côté plateforme. En OIDC, c’est une déclaration explicite : le
  // jeton de connexion ne peut cloner un dépôt que si la source d’identité est aussi
  // un serveur git.
  gitShapeCustomLabel: 'Forme de la plateforme',
  gitShapeOidcLabel: 'Est aussi une plateforme git',
  gitShapeOidcOptional: '(vide = identité seule)',
  gitShapeNone: 'Non déclaré',
  gitShapeCustomHint:
    'Les URL d’autorisation / de jeton / d’utilisateur d’une instance auto-hébergée sont dérivées de la plateforme choisie : GitLab utilise /oauth/authorize, Gitea utilise /login/oauth/authorize. Un mauvais choix pointe vers un chemin inexistant.',
  gitShapeOidcHint:
    'Un GitLab / Gitea auto-hébergé peut aussi servir de source OIDC. Ce n’est qu’après la déclaration que le jeton obtenu à la connexion peut cloner un dépôt et que l’option « Synchroniser un identifiant git à la connexion » ci-dessous s’active. En cas de doute, laissez vide et utilisez-la comme identité seule.',
  errGitShapeInvalid:
    'La forme de la plateforme doit rester vide ou valoir gitlab ou gitea : avec toute autre valeur, l’URL d’autorisation construite pointera vers un chemin inexistant',

  // ─── Sync des identifiants à la connexion (les 5 plateformes git + l’OIDC
  // déclarant une forme git). ─────────────────────────────────────────────────
  // C’est ici que se joue tout ce qu’une seule connexion emporte : désactivé par
  // défaut, et le texte dit à la fois la portée gagnée et comment s’en sortir.
  syncTitle: 'Synchroniser un identifiant git à la connexion',
  syncDesc: 'Après la connexion via cette plateforme, enregistrer le jeton d’accès obtenu de cette autorisation comme identifiant git personnel de ce compte',
  syncAria: 'Activer la synchronisation à la connexion pour {provider}',
  syncNote:
    'Une fois activé, la connexion demande davantage : de « identité seule » à « lire les dépôts ». Avant l’expiration, la plateforme renouvelle le jeton elle-même ; si le renouvellement est refusé, l’identifiant est marqué « autorisation révoquée » et une nouvelle connexion le rétablit. L’identifiant synchronisé reste visible uniquement pour ce compte — l’identifiant utilisé par un projet vous appartient toujours.',
  syncNeedsShapeHint:
    'Cette source OIDC n’a pas encore déclaré la plateforme git qui se trouve derrière elle : l’option reste donc verrouillée. Choisissez-en une dans « Est aussi une plateforme git » pour pouvoir parler de synchroniser un identifiant.',
  errSyncUnsupported:
    'Cette source d’identité ne peut pas délivrer de jeton permettant de cloner un dépôt : la synchronisation à la connexion n’est ouverte qu’aux plateformes git (Gitee / GitHub / GitLab / Gitea / auto-hébergée) ou à un fournisseur OIDC déclarant une forme git',
  errSyncNeedsGitPlatform:
    'Ce fournisseur d’identité OIDC n’a pas déclaré la plateforme git qu’il front (GitLab / Gitea), « Synchroniser un identifiant git à la connexion » ne peut donc pas être activé : choisissez-en une sous « Est aussi une plateforme git », ou laissez l’option désactivée',

  // ─── Source d’identité OIDC (uniquement sous « Connexion à la plateforme ») ──
  // Le backend résout réellement les points d’accès à l’enregistrement : une
  // mauvaise configuration sinon se verrait comme « l’écran de consentement passe,
  // puis ça casse au retour ». Mieux vaut échouer au moment de sauvegarder.
  providerOidcDesc: 'OIDC / SSO générique (Keycloak, Authentik…)',
  oidcDiscoveryLabel: 'URL de Discovery',
  oidcDiscoveryOptional: '(celle-ci ou les trois points d’accès ci-dessous)',
  oidcDiscoveryPlaceholder: 'https://sso.example.com/realms/my/.well-known/openid-configuration',
  oidcDiscoveryHint:
    'Renseignée, les points d’accès autorisation / jeton / userinfo viennent du document ; l’enregistrement va le chercher pour de vrai et échoue sur place sinon.',
  oidcOverrideOptional: '(vide = reprendre le document)',
  oidcAuthorizeLabel: 'Authorization Endpoint',
  oidcTokenLabel: 'Token Endpoint',
  oidcUserinfoLabel: 'Userinfo Endpoint',
  oidcEndpointPlaceholder: 'https://sso.example.com/…',
  oidcEndpointsHint:
    'Si le document est injoignable (réseau isolé, passerelle qui bloque), renseignez les trois à la main ; la valeur saisie prime sur le document.',
  oidcIdClaimLabel: 'Claim identifiant',
  oidcIdClaimOptional: '(vide = sub)',
  oidcIdClaimHint:
    'Quel claim identifie le compte de façon unique. Par défaut sub, que la spécification veut immuable. Prendre l’e-mail ou le nom d’utilisateur, c’est laisser un changement futur atterrir sur le compte de quelqu’un d’autre.',
  oidcScopesLabel: 'Scopes',
  oidcScopesOptional: '(vide = openid profile email)',
  oidcScopesHint:
    'Séparés par des espaces ; openid est obligatoire. Sans e-mail renvoyé, l’allowlist de domaines bloque la connexion.',
  oidcUserinfoNote:
    'L’identité est lue sur le point d’accès userinfo : cette version ne vérifie ni la signature ni l’audience de id_token, donc ne branchez qu’un fournisseur interne ou en HTTPS.',
  errOidcEndpointsIncomplete:
    'Pour activer OIDC, donnez une URL de Discovery ou renseignez les trois points d’accès autorisation / jeton / userinfo',
  errOidcConfigInvalid:
    'Points d’accès non résolus : URL incorrecte, discovery injoignable ou les trois points d’accès sont incomplets (la configuration enregistrée est restée intacte)',

  // ─── DingTalk / Feishu / WeCom (uniquement sous « Connexion plateforme ») ───
  // Les points d’accès officiels sont préchargés côté serveur ; la page ne propose
  // que des remplacements, utiles pour un proxy interne ou une installation privée.
  providerDingTalkDesc: 'Connexion par QR code DingTalk',
  providerFeishuDesc: 'Connexion par QR code Feishu',
  providerWecomDesc: 'Connexion par QR code WeCom (application créée par vous)',
  clientIdPlaceholderPlatform:
    'Le Client ID indiqué dans la fiche de l’application (pour WeCom, l’ID d’entreprise / corpid)',
  callbackHintPlatform:
    'Saisissez cette URL dans le champ « callback / URL de redirection » de l’application ; WeCom exige aussi que le domaine de cette instance soit déclaré comme domaine de confiance.',
  oidcScopesPlaceholder: 'openid profile email',
  platformOverrideOptional: '(vide = l’adresse officielle)',
  platformEndpointPlaceholder: 'https://… (utile seulement pour un proxy interne ou une installation privée)',
  platformEndpointsHint:
    'Les adresses autorisation / jeton / identité sont déjà préchargées avec les valeurs officielles et restent vides en général ; ne les remplacez que pour un proxy interne ou une installation privée, la valeur saisie prime alors.',
  agentIdPlaceholder: 'L’AgentId visible dans les détails de l’application WeCom, par ex. 1000002',
  agentIdHint:
    'L’URL de scan en a besoin (le paramètre s’appelle agentid) pour résoudre l’identité du membre ; le champ Client ID reçoit l’ID d’entreprise (corpid), pas ce numéro.',
  platformScopesOptional: '(vide = la valeur par défaut)',
  platformScopesPlaceholder: 'séparés par des espaces, par ex. contact:user.base:readonly',
  platformScopesHint:
    'DingTalk demande openid par défaut ; Feishu ne demande aucun scope par défaut, car une permission non activée pour l’application est refusée immédiatement. Pour filtrer sur la liste de domaines e-mail, la plateforme doit renvoyer un e-mail.',
  platformIdentityNote:
    'Les comptes sont appariés uniquement sur l’identifiant stable émis par la plateforme (DingTalk / Feishu : unionId, à défaut openId ; WeCom : ID d’entreprise plus userid du membre). Le pseudonyme et l’e-mail n’identifient jamais un compte, donc un changement de nom ne peut pas atterrir sur le compte de quelqu’un d’autre.',
  errAgentIdRequired: 'WeCom nécessite un AgentId (dans les détails de l’application)',
  errPlatformConfigInvalid:
    'Les remplacements de points d’accès doivent être des URL http(s) sans identifiants intégrés ; laissez vide pour utiliser les adresses officielles (la configuration enregistrée est restée intacte)',
}

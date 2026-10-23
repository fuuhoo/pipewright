export default {
  identityTitle: 'Identité',
  identitySub: 'Ceci est votre compte local sur cette instance. Le rôle décide des pages accessibles ; il est attribué par un administrateur dans la gestion des utilisateurs.',

  // ─── Profil local (nom affiché + e-mail) ───────────────────────────────────
  // Ces deux champs vivent sur la ligne users : copiés à la première connexion via une
  // identité externe, puis gérés par vous ; les connexions suivantes n’écrasent plus rien.
  profileTitle: 'Profil local',
  profileSub: 'Votre nom affiché et votre e-mail sont enregistrés sur ce compte. Ils ont été repris à votre première connexion avec une identité externe ; vous les gérez ici, et les connexions suivantes ne les écraseront plus.',
  displayNameLabel: 'Nom affiché',
  displayNameHint: 'Le nom montré en haut à droite et dans les listes d’utilisateurs. Les espaces consécutifs sont fusionnés ; {n} caractères au maximum. Laissez vide pour afficher votre identifiant de connexion.',
  emailLabel: 'E-mail',
  emailHint: 'Sert uniquement à vous identifier dans cette instance ; rien n’est envoyé. Peut rester vide.',
  identityNoEmailSet: 'aucun e-mail défini',
  profileSaveBtn: 'Enregistrer le profil',
  errEmailSpace: 'L’e-mail ne peut pas contenir d’espace',
  errEmailFormat: "Cet e-mail est invalide ; il devrait ressembler à name{'@'}example.com",
  errEmailTooLong: 'Cet e-mail est trop long',
  toastProfileSaved: 'Profil enregistré',
  toastProfileSavedDetail: 'La vignette en haut à droite et cette page utilisent désormais le nouveau profil',
  toastProfileFailed: 'Échec de l’enregistrement du profil',

  changePwTitle: 'Changer le mot de passe',
  changePwSub: 'Après la mise à jour, les autres sessions sont révoquées tandis que la session actuelle reste connectée.',
  currentPwLabel: 'Mot de passe actuel',
  newPwLabel: 'Nouveau mot de passe',
  newPwHint: 'Au moins 8 caractères',
  confirmPwLabel: 'Confirmer le nouveau mot de passe',
  changePwBtn: 'Changer le mot de passe',

  errCurrentRequired: 'Veuillez saisir votre mot de passe actuel',
  errNewTooShort: 'Le nouveau mot de passe doit comporter au moins 8 caractères',
  errConfirmMismatch: 'Les deux nouveaux mots de passe ne correspondent pas',
  errCurrentWrong: 'Le mot de passe actuel est incorrect',

  toastPwUpdated: 'Mot de passe mis à jour',
  toastPwUpdatedDetail: 'Les autres sessions ont été révoquées ; la session actuelle reste connectée',
  toastPwFailed: 'Échec de la mise à jour du mot de passe',
  unknownError: 'Erreur inconnue',

  sessionsTitle: 'Sessions actives',
  sessionsSub: 'Sessions côté serveur de ce compte (expiration glissante de 7 jours). Révoquer une session déconnecte immédiatement son appareil.',
  errLoadSessions: 'Impossible de charger les sessions',
  retry: 'Réessayer',
  emptyTitle: 'Aucune session active',
  emptyDesc: 'Les sessions apparaîtront ici après la connexion.',
  currentSession: 'Session actuelle',
  sessionMeta: 'Connecté le {created} · Dernière activité {lastSeen}',
  revokeBtn: 'Révoquer',

  revokeCurrentTitle: 'Révoquer la session actuelle ?',
  revokeTitle: 'Révoquer cette session ?',
  revokeCurrentBody: 'La révocation de la session actuelle vous déconnecte et vous devrez vous reconnecter.',
  revokeBody: 'Cet appareil/navigateur sera déconnecté immédiatement et devra se reconnecter pour accéder à nouveau.',
  revokeConfirm: 'Révoquer la session',
  toastCurrentRevoked: 'Session actuelle révoquée',
  toastCurrentRevokedDetail: 'Redirection vers la connexion',
  toastSessionRevoked: 'Session révoquée',
  toastRevokeFailed: 'Échec de la révocation de la session',

  onboardingTitle: 'Guide de première utilisation',
  onboardingSub: 'Rouvrez à tout moment le guide en trois étapes « Connecter l’IA → Ajouter un serveur → Créer un projet ».',
  onboardingBtn: 'Relancer le guide',
  toastOnboardingReset: 'Guide réinitialisé',
  toastOnboardingResetDetail: 'Ouverture du guide de première utilisation',

  // ─── comptes git reliés (connexion avec le compte de la plateforme) ───
  // Un lien n’est que l’association « (plateforme, ID stable) ↔ ce compte » :
  // il ne change pas le rôle, et rompre le lien ne déconnecte pas la session en cours.
  identitiesTitle: 'Comptes externes reliés',
  identitiesSub: 'Le lien décide seulement quel compte de la plateforme peut se connecter à cette instance ; il ne change pas votre rôle, et le rompre ne déconnecte pas cette session.',
  identitiesEmpty: 'Aucun compte externe relié',
  identitiesEmptyDesc: 'Une fois relié, vous pouvez vous connecter à cette instance avec ce compte de plateforme au lieu d’un mot de passe.',
  errLoadIdentities: 'Impossible de charger la liste des liens',
  identityMeta: 'E-mail {email} · ID plateforme {externalId}',
  // État de la synchronisation : affiché seulement quand cette correspondance a
  // vraiment produit un identifiant (la synchronisation est fermée par défaut).
  syncLead: 'Un identifiant git personnel a été synchronisé à la connexion (voir « Mes identifiants »)',
  syncExpires: 'Jeton expirant le {time}',
  syncScope: 'Portée réellement accordée {scope}',
  identityNoEmail: 'non fourni',
  unbindBtn: 'Rompre',
  unbindTitle: 'Rompre ce lien ?',
  unbindBody: '{login} sur {provider} ne pourra plus se connecter à cette instance. Votre session en cours reste ouverte.',
  unbindConfirm: 'Rompre le lien',
  toastUnbound: 'Lien rompu',
  toastAlreadyUnbound: 'Ce lien n’existe plus ; la liste a été rechargée',
  toastUnbindFailed: 'Échec de la rupture du lien',
  bindHint: 'Relier un autre compte de plateforme git',
  bindBtn: 'Relier avec {provider}',
  toastBound: 'Relié avec {provider}',
  toastBindFailed: 'La liaison avec {provider} n’a pas abouti',
  providerCustom: 'Auto-hébergé',
}

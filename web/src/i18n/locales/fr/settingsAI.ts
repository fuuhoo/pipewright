export default {
  title: 'Fournisseur AI',
  subtitle:
    'Pipewright n’entraîne aucun modèle propre — connectez votre propre LLM pour le diagnostic des échecs et la génération de configuration. Chaque protocole conserve ses propres réglages et un seul est actif à la fois. Les clés ne résident que dans le coffre chiffré de cette instance et n’en sortent jamais.',
  statusConfigured: 'Configuré',
  statusUnconfigured: 'Non configuré',
  activeBadge: 'En cours d’utilisation',
  retry: 'Réessayer',

  providerClaudeTag: 'Recommandé pour le diagnostic',
  providerOllamaDesc: 'Local / auto-hébergé',
  providerOllamaTag: 'Aucune sortie de données',
  protocolClaude: 'Protocole Claude',
  protocolOpenAI: 'Protocole OpenAI',
  protocolOllama: 'Protocole Ollama',
  protocolOpenAIDesc: 'GPT-4o, DeepSeek et autres points d’accès compatibles',

  guidanceAria: 'Guide de configuration AI',
  guidanceTitle: 'Configurez un LLM pour activer le diagnostic AI',
  guidanceBody:
    'Une fois le protocole Claude, le protocole OpenAI (DeepSeek et autres points d’accès compatibles) ou un Ollama local connecté, Pipewright génère automatiquement des hypothèses de cause racine et des suggestions de correction lorsqu’un pipeline échoue, sans avoir à fouiller les journaux manuellement.',

  selectProvider: 'Sélectionnez un protocole',
  providerRadioAria: 'Sélection du protocole AI',
  selectProviderAria: 'Sélectionner {name}',
  providerConfig: 'Configuration du {name}',
  lastSaved: 'Dernier enregistrement {time}',

  apiKeyHint: 'Seule une valeur masquée s’affiche après l’enregistrement ; laissez vide pour conserver la clé existante',
  apiKeyReplacing: 'Remplacement…',
  apiKeyConfigured: 'Configurée •••• (vide pour conserver)',
  apiKeyPaste: 'Collez la API Key…',
  apiKeyMaskedAria: 'Masque configuré : {masked}',

  ollamaHint: 'Ollama local ne nécessite aucune API Key — assurez-vous simplement que le service Ollama est en cours d’exécution à l’adresse indiquée.',

  baseUrlLabel: 'Base URL',
  baseUrlHint: 'Par défaut : {url}',
  presetLabel: 'Points d’accès courants',
  presetApplyAria: 'Renseigner le point d’accès et le modèle avec le préreset {name}',

  modelLabel: 'Modèle',
  modelHint: 'Modèle principal utilisé pour le diagnostic, par ex. claude-opus-4-7 / gpt-4o / deepseek-chat / llama3',

  testConnection: 'Tester la connexion',
  testOk: 'Connexion correcte · latence {ms}ms',
  testFail: 'Échec de la connexion',

  budgetLabel: 'Limite mensuelle de Token',
  budgetHint: 'Met en pause le diagnostic AI une fois dépassée (vide = illimité ; déclarée ce cycle, appliquée au prochain Epic)',
  budgetPlaceholder: 'par ex. 500000, vide = illimité',
  usedThisMonth: 'Ce mois-ci · {prompt} en entrée / {completion} en sortie',
  usedNone: 'Aucune utilisation ce mois-ci',

  enableAi: 'Définir comme protocole actif',
  enableAiDesc: 'Le diagnostic et la génération utilisent ce protocole ; l’activer désactive automatiquement les autres',

  dirtyNote: 'Ce protocole a des modifications non enregistrées',
  cleanNote: 'Aucune modification sur ce protocole',
  discard: 'Abandonner',
  saveChanges: 'Enregistrer les modifications',

  toastSaveSuccess: 'Réglages du protocole enregistrés',
  toastSaveFailed: 'Échec de l’enregistrement',

  errServerUnreachable: 'Impossible de joindre le serveur. Vérifiez que le backend est en cours d’exécution et réessayez.',
  errServerUnreachableShort: 'Impossible de joindre le serveur',
  errVaultUnconfigured: 'Le coffre n’a pas de master key configurée. Définissez la variable d’environnement PIPEWRIGHT_MASTER_KEY.',
  errLoadFailed: 'Échec du chargement ({status})',
  errLoadGeneric: 'Échec du chargement de la configuration AI. Veuillez réessayer plus tard.',
  errBudgetInvalid: 'La limite mensuelle de tokens doit être un entier positif ou laissée vide',
  errProviderInvalid: 'Veuillez sélectionner un protocole valide',
  errBaseUrlRequired: 'Veuillez saisir la base URL',
  errApiKeyRequired: 'La API Key ne peut pas être vide (obligatoire sauf pour Ollama)',
  errRequestFailed: 'Échec de la requête ({status})',
  errUnknown: 'Erreur inconnue',
}

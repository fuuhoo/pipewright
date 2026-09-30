export default {
  title: 'AI-Anbieter',
  subtitle:
    'Pipewright trainiert keine eigenen Modelle – binde dein eigenes LLM für die Fehlerdiagnose und Konfigurationserstellung ein. Jedes Protokoll führt seine eigenen Einstellungen; aktiv sein kann immer nur eines. Schlüssel liegen ausschließlich im verschlüsselten Tresor dieser Instanz und verlassen ihn nie.',
  statusConfigured: 'Konfiguriert',
  statusUnconfigured: 'Nicht konfiguriert',
  activeBadge: 'In Verwendung',
  retry: 'Erneut versuchen',

  providerClaudeTag: 'Empfohlen für Diagnose',
  providerOllamaDesc: 'Lokal / selbst gehostet',
  providerOllamaTag: 'Kein Datenabfluss',
  protocolClaude: 'Claude-Protokoll',
  protocolOpenAI: 'OpenAI-Protokoll',
  protocolOllama: 'Ollama-Protokoll',
  protocolOpenAIDesc: 'GPT-4o, DeepSeek und andere kompatible Endpoints',

  guidanceAria: 'AI-Konfigurationsleitfaden',
  guidanceTitle: 'Konfiguriere ein LLM, um die AI-Diagnose freizuschalten',
  guidanceBody:
    'Sobald du das Claude-Protokoll, das OpenAI-Protokoll (DeepSeek und andere kompatible Endpoints) oder ein lokales Ollama verbindest, generiert Pipewright bei einem Pipeline-Fehler automatisch Ursachenhypothesen und Korrekturvorschläge – ohne manuelles Durchsuchen der Logs.',

  selectProvider: 'Protokoll auswählen',
  providerRadioAria: 'Auswahl des AI-Protokolls',
  selectProviderAria: '{name} auswählen',
  providerConfig: '{name} — Konfiguration',
  lastSaved: 'Zuletzt gespeichert {time}',

  apiKeyHint: 'Nach dem Speichern wird nur ein maskierter Wert angezeigt; leer lassen, um den vorhandenen Schlüssel zu behalten',
  apiKeyReplacing: 'Wird ersetzt…',
  apiKeyConfigured: 'Konfiguriert •••• (leer lassen = unverändert)',
  apiKeyPaste: 'API Key einfügen…',
  apiKeyMaskedAria: 'Konfigurierte Maske: {masked}',

  ollamaHint: 'Lokales Ollama benötigt keinen API Key – stelle nur sicher, dass der Ollama-Dienst unter der angegebenen Adresse läuft.',

  baseUrlLabel: 'Base URL',
  baseUrlHint: 'Standard: {url}',
  presetLabel: 'Übliche Endpoints',
  presetApplyAria: 'Endpoint und Modell mit {name}-Vorgabe ausfüllen',

  modelLabel: 'Modell',
  modelHint: 'Hauptmodell für die Diagnose, z. B. claude-opus-4-7 / gpt-4o / deepseek-chat / llama3',

  testConnection: 'Verbindung testen',
  testOk: 'Verbindung OK · Latenz {ms}ms',
  testFail: 'Verbindung fehlgeschlagen',

  budgetLabel: 'Monatliches Token-Limit',
  budgetHint: 'Pausiert die AI-Diagnose bei Überschreitung (leer = unbegrenzt; in diesem Zyklus nur deklariert, im nächsten Epic erzwungen)',
  budgetPlaceholder: 'z. B. 500000, leer = unbegrenzt',
  usedThisMonth: 'Diesen Monat · {prompt} Eingabe / {completion} Ausgabe',
  usedNone: 'In diesem Monat noch keine Nutzung',

  enableAi: 'Als aktives Protokoll setzen',
  enableAiDesc: 'Diagnose und Generator nutzen dieses Protokoll; beim Aktivieren werden die anderen automatisch deaktiviert',

  dirtyNote: 'Dieses Protokoll hat ungespeicherte Änderungen',
  cleanNote: 'Keine Änderungen an diesem Protokoll',
  discard: 'Verwerfen',
  saveChanges: 'Änderungen speichern',

  toastSaveSuccess: 'Protokolleinstellungen gespeichert',
  toastSaveFailed: 'Speichern fehlgeschlagen',

  errServerUnreachable: 'Server nicht erreichbar. Prüfe, ob das Backend läuft, und versuche es erneut.',
  errServerUnreachableShort: 'Server nicht erreichbar',
  errVaultUnconfigured: 'Im Tresor ist kein Master Key konfiguriert. Setze die Umgebungsvariable PIPEWRIGHT_MASTER_KEY.',
  errLoadFailed: 'Laden fehlgeschlagen ({status})',
  errLoadGeneric: 'AI-Einstellungen konnten nicht geladen werden. Bitte später erneut versuchen.',
  errBudgetInvalid: 'Das monatliche Token-Limit muss eine positive Ganzzahl oder leer sein',
  errProviderInvalid: 'Bitte ein gültiges Protokoll auswählen',
  errBaseUrlRequired: 'Bitte die Base URL eingeben',
  errApiKeyRequired: 'API Key darf nicht leer sein (außer bei Ollama erforderlich)',
  errRequestFailed: 'Anfrage fehlgeschlagen ({status})',
  errUnknown: 'Unbekannter Fehler',
}

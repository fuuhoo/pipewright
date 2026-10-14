export default {
  title: 'OAuth-Apps',
  sectionDesc:
    'Registriere für jede git-Plattform eine OAuth-App (Client ID / Secret), damit Benutzer ihr Konto im Anmeldedaten-Tresor mit einem Klick „Verbinden“ und automatisch ein Token erhalten können, ohne manuell ein PAT einzufügen. Das Secret wird beim Speichern maskiert und kann nach dem Schreiben nicht mehr gelesen werden. Dieselbe Plattform lässt sich für einen zweiten Zweck registrieren: Besucher sollen sich mit ihrem Plattform-Konto bei dieser Instanz anmelden.',
  tabsAria: 'OAuth-Anbieter',
  retry: 'Erneut versuchen',

  providerCustomLabel: 'Selbst gehostet',
  providerGiteeDesc: 'Gitee',
  providerCustomDesc: 'Selbst gehostetes GitLab / Gitea usw.',

  statusEnabled: 'Aktiviert',
  statusConfiguredIdle: 'Konfiguriert · Nicht aktiviert',
  statusUnconfigured: 'Nicht konfiguriert',

  clientIdPlaceholder: 'Auf der OAuth-App-Seite der git-Plattform abrufen',
  secretOptional: '(nach dem Schreiben wird nur die Maske angezeigt)',
  secretPlaceholderKeep: 'Leer lassen, um das vorhandene Secret beizubehalten',
  secretPlaceholderNew: 'Client Secret einfügen…',
  secretStored: 'Gespeichert: {masked}',

  toggleAria: '{provider} OAuth aktivieren',
  toggleTitle: 'Verbindung aktivieren',
  toggleDesc: 'Nach der Aktivierung erscheint die Schaltfläche „Verbinden“ dieser Plattform im Anmeldedaten-Tresor',

  lastSaved: 'Zuletzt gespeichert {time}',
  saveBtn: 'Speichern',

  errNoServer: 'Server nicht erreichbar. Bitte prüfe, ob das Backend läuft, und versuche es erneut',
  errVaultUnconfigured: 'Für den Tresor ist kein master key konfiguriert. Bitte setze die Umgebungsvariable PIPEWRIGHT_MASTER_KEY',
  errLoadStatus: 'Laden fehlgeschlagen ({status})',
  errLoadGeneric: 'OAuth-Apps konnten nicht geladen werden. Bitte versuche es später erneut',

  errClientIdRequiredEnabled: 'Bei Aktivierung ist die Client ID erforderlich',
  errSecretRequiredEnabled: 'Bei Aktivierung ist ein Client Secret erforderlich',
  errBaseUrlRequiredCustom: 'Eine selbst gehostete Instanz erfordert eine Base URL',
  errClientIdRequired: 'Bitte gib die Client ID ein',
  errBaseUrlRequired: 'Bitte gib die Base URL ein',

  toastSaveFailed: 'Speichern fehlgeschlagen',
  toastSaved: 'OAuth-App gespeichert',
  errNoServerShort: 'Server nicht erreichbar',
  unknownError: 'Unbekannter Fehler',

  justNow: 'gerade eben',
  minutesAgo: 'vor {n} Minuten',
  hoursAgo: 'vor {n} Stunden',
  daysAgo: 'vor {n} Tagen',

  // ─── Zweck-Umschalter: zwei Konfigurationen pro Plattform (Repository-Zugangsdaten / Plattform-Anmeldung) ───
  // Zwei Konfigurationen, zwei Callback-URLs: Eine OAuth-App registriert üblicherweise
  // nur eine Callback-URL, beide Zwecke sich ihn zu teilen würde sie blockieren.
  purposeSwitchAria: 'OAuth-Zweck',
  purposeVault: 'Repository-Zugangsdaten',
  purposeVaultHint: 'Angemeldete Benutzer holen nach der Freigabe ein Git-Token in den Tresor',
  purposeLogin: 'Plattform-Anmeldung',
  purposeLoginHint: 'Besucher ohne Sitzung melden sich mit ihrem Plattform-Konto an; automatisch angelegte Konten erhalten nur die kleinste Rolle',
  purposeVaultNote:
    'Diese Konfiguration steuert nur die Schaltfläche „Git-Konto verbinden“. Was du auf der Plattformseite eintragen musst, ist die Callback-URL unten — änderst du sie, auch dort nachziehen.',
  purposeLoginNote:
    'Nach der Aktivierung erscheint dieser Anbieter auf der Anmeldeseite. Jeder Zweck braucht eine eigene OAuth-App: Eine App akzeptiert nur eine Callback-URL.',
  callbackHint: 'Trage diese URL bei der OAuth-App deiner git-Plattform als „Callback / Redirect-URI“ ein',
  callbackCopy: 'Klicken zum Alles-auswählen, danach kopieren und auf der Plattformseite eintragen',
  domainsLabel: 'E-Mail-Domain-Allowlist',
  domainsOptional: '(leer = jede Domain)',
  domainsPlaceholder: 'corp.example.com, hr.example.com',
  domainsHint:
    'Nur Konten mit einer E-Mail in diesen Domains dürfen sich anmelden. Ist die Allowlist leer, erhält jede E-Mail automatisch ein Konto (weiterhin nur mit der kleinsten Rolle).',
  errDomainsInvalid:
    'Die E-Mail-Allowlist enthält einen ungültigen Eintrag: gib nur die Domain selbst an (z. B. corp.example.com), ohne Protokoll, Pfad oder Leerzeichen',
  toggleTitleLogin: 'Anmeldung aktivieren',
  toggleDescLogin: 'Nach der Aktivierung zeigt die Anmeldeseite eine Schaltfläche „Mit diesem Plattform-Konto anmelden“',
}

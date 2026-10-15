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

  // ─── OIDC-Identitätsquelle (erscheint nur unter „Plattform-Anmeldung“) ──────
  // Beim Speichern löst das Backend die Endpunkte tatsächlich auf: eine kaputte
  // Konfiguration würde sich sonst erst als „Consent-Seite lief, beim Rückweg
  // kippte es“ zeigen — am Speichern zu scheitern ist der freundlichere Ort.
  providerOidcDesc: 'Generisches OIDC / SSO (Keycloak, Authentik …)',
  oidcDiscoveryLabel: 'Discovery-URL',
  oidcDiscoveryOptional: '(entweder diese oder die drei Endpunkte unten)',
  oidcDiscoveryPlaceholder: 'https://sso.example.com/realms/my/.well-known/openid-configuration',
  oidcDiscoveryHint:
    'Ausgefüllt, stammen die Endpunkte für Autorisierung / Token / Nutzerinfo aus dem Dokument; beim Speichern wird es tatsächlich abgerufen, sonst kommt sofort ein Fehler.',
  oidcOverrideOptional: '(leer = Dokumentwert verwenden)',
  oidcAuthorizeLabel: 'Authorization Endpoint',
  oidcTokenLabel: 'Token Endpoint',
  oidcUserinfoLabel: 'Userinfo Endpoint',
  oidcEndpointPlaceholder: 'https://sso.example.com/…',
  oidcEndpointsHint:
    'Ist das Dokument unerreichbar (abgeschirmtes Netz, Gateway blockiert), alle drei per Hand ausfüllen — der per Hand eingetragene Wert gewinnt.',
  oidcIdClaimLabel: 'ID-Claim',
  oidcIdClaimOptional: '(leer = sub)',
  oidcIdClaimHint:
    'Welcher Claim das Konto eindeutig kennzeichnet. Standard ist sub, das laut Spezifikation unveränderlich sein muss. E-Mail oder Benutzername führt dazu, dass eine spätere Umbenennung auf ein fremdes Konto fällt.',
  oidcScopesLabel: 'Scopes',
  oidcScopesOptional: '(leer = openid profile email)',
  oidcScopesHint:
    'Leerzeichengetrennt; openid ist Pflicht. Kommt keine E-Mail zurück, blockiert die E-Mail-Domain-Allowlist die Anmeldung.',
  oidcUserinfoNote:
    'Die Identität kommt vom userinfo-Endpunkt: Diese Version prüft weder Signatur noch Audience des id_token, daher nur interne oder HTTPS-Anbieter anbinden.',
  errOidcEndpointsIncomplete:
    'Zum Aktivieren von OIDC eine Discovery-URL angeben oder alle drei Endpunkte für Autorisierung / Token / Nutzerinfo ausfüllen',
  errOidcConfigInvalid:
    'Endpunkte konnten nicht aufgelöst werden: falsche URL, Discovery nicht erreichbar oder die drei Endpunkte unvollständig (die gespeicherte Konfiguration blieb unberührt)',
}

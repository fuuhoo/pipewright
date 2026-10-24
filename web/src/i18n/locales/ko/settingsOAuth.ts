export default {
  title: 'OAuth 앱',
  sectionDesc:
    '각 git 플랫폼에 OAuth 앱(Client ID / Secret)을 등록하면 사용자는 자격 증명 보관소에서 원클릭으로 계정을 "연결"하여 PAT를 수동으로 붙여넣지 않고도 토큰을 자동으로 발급받을 수 있습니다. Secret은 저장 시 마스킹되며 작성 후에는 다시 읽을 수 없습니다. 같은 플랫폼은 두 번째 용도로도 등록할 수 있습니다. 방문자가 플랫폼 계정으로 이 인스턴스에 로그인하게 하는 것입니다. 딩톡, 페이슈, 기업위챗, 범용 OIDC는 두 번째 용도로만 존재하며 자격 증명 보관소에는 나타나지 않습니다.',
  tabsAria: 'OAuth 제공자',
  retry: '다시 시도',

  providerCustomLabel: '자체 호스팅',
  providerGiteeDesc: 'Gitee',
  providerCustomDesc: '자체 호스팅 GitLab / Gitea 등',

  statusEnabled: '활성화됨',
  statusConfiguredIdle: '구성됨 · 비활성',
  statusUnconfigured: '구성 안 됨',

  clientIdPlaceholder: 'git 플랫폼의 OAuth 앱 페이지에서 가져오기',
  clientIdPlaceholderOidc: 'ID 공급자에 OIDC 클라이언트를 등록하면 발급됩니다',
  secretOptional: '(작성 후에는 마스크만 표시)',
  secretPlaceholderKeep: '비워 두면 기존 Secret을 유지합니다',
  secretPlaceholderNew: 'Client Secret 붙여넣기…',
  secretStored: '저장됨: {masked}',

  toggleAria: '{provider} OAuth 활성화',
  toggleTitle: '연결 활성화',
  toggleDesc: '활성화하면 이 플랫폼의 "연결" 버튼이 자격 증명 보관소에 표시됩니다',

  lastSaved: '마지막 저장 {time}',
  saveBtn: '저장',

  errNoServer: '서버에 연결할 수 없습니다. 백엔드 실행 여부를 확인한 후 다시 시도하세요',
  errVaultUnconfigured: '보관소에 master key가 구성되지 않았습니다. PIPEWRIGHT_MASTER_KEY 환경 변수를 설정하세요',
  errLoadStatus: '불러오기 실패({status})',
  errLoadGeneric: 'OAuth 앱을 불러오지 못했습니다. 잠시 후 다시 시도하세요',

  errClientIdRequiredEnabled: '활성화 시 Client ID는 필수입니다',
  errSecretRequiredEnabled: '활성화 시 Client Secret이 필요합니다',
  errBaseUrlRequiredCustom: '자체 호스팅 인스턴스에는 Base URL이 필요합니다',
  errClientIdRequired: 'Client ID를 입력하세요',
  errBaseUrlRequired: 'Base URL을 입력하세요',

  toastSaveFailed: '저장 실패',
  toastSaved: 'OAuth 앱이 저장되었습니다',
  errNoServerShort: '서버에 연결할 수 없습니다',
  unknownError: '알 수 없는 오류',

  justNow: '방금 전',
  minutesAgo: '{n}분 전',
  hoursAgo: '{n}시간 전',
  daysAgo: '{n}일 전',

  // ─── 용도 전환: 한 플랫폼에 두 벌의 설정(저장소 자격 증명 / 플랫폼 로그인)───
  // 설정도 callback URL도 별개입니다. OAuth 앱에는 보통 callback 하나만 등록할 수 있어,
  // 두 용도가 하나를 공유하면 서로 막힙니다.
  purposeSwitchAria: 'OAuth 용도',
  purposeVault: '저장소 자격 증명',
  purposeVaultHint: '로그인한 사용자가 인증하면 git 토큰이 보관소에 저장됩니다',
  purposeLogin: '플랫폼 로그인',
  purposeLoginHint: '로그인하지 않은 방문자가 플랫폼 계정으로 이 인스턴스에 로그인합니다. 자동 생성 계정은 최소 권한만 받습니다',
  purposeVaultNote:
    '이 설정은 "git 계정 연결" 버튼만 제어합니다. 플랫폼 쪽에 등록할 대상은 아래 Callback URL입니다. 바꾸면 그 앱도 함께 수정하세요.',
  purposeLoginNote:
    '활성화하면 이 제공자가 로그인 화면에 나타납니다. 용도별로 OAuth 앱을 따로 만드세요. 앱 하나에는 callback URL 한 개만 등록할 수 있습니다.',
  callbackHint: 'git 플랫폼의 OAuth 앱에서 이 URL을 "callback / Redirect URI"에 붙여넣으세요',
  callbackHintOidc: 'ID 공급자의 OIDC 클라이언트에서 이 URL을 "callback / Redirect URI"에 붙여넣으세요',
  callbackCopy: '클릭하면 전체 선택, 복사 후 플랫폼 쪽에 등록하세요',
  domainsLabel: '이메일 도메인 허용 목록',
  domainsOptional: '(비우면 도메인 제한 없음)',
  domainsPlaceholder: 'corp.example.com, hr.example.com',
  domainsHint:
    '이 도메인에 속한 이메일을 가진 계정만 로그인할 수 있습니다. 목록을 비우면 어떤 이메일로도 계정이 자동 생성됩니다(역시 최소 권한만 부여).',
  errDomainsInvalid:
    '허용 목록에 잘못된 항목이 있습니다. 도메인만 입력하세요(예: corp.example.com). 프로토콜·경로·공백은 넣지 마세요',
  toggleTitleLogin: '로그인 활성화',
  toggleDescLogin: '활성화하면 로그인 화면에 "이 플랫폼 계정으로 로그인" 단추가 나타납니다',

  // ─── git 형태(자체 호스팅은 엔드포인트 형태 / OIDC는 "동시에 git 플랫폼" 선언)─────
  // 같은 칸이라도 의미가 둘입니다. 자체 호스팅은 "어느 회사 주소로짜느냐"의 선택이라
  // 잘못 고르면 인증 화면이 플랫폼 쪽에 없습니다. OIDC에서의 이 칸은 명시적 선언이고,
  // 이 신원 출처가 실제로 git 플랫폼도 겸해야만 로그인 토큰을 복사에 쓸 수 있습니다.
  gitShapeCustomLabel: '플랫폼 형태',
  gitShapeOidcLabel: '동시에 git 플랫폼',
  gitShapeOidcOptional: '(비우면 신원 확인만)',
  gitShapeNone: '선언 안 함',
  gitShapeCustomHint:
    '자체 호스팅 인스턴스의 인증 / 토큰 / 사용자 정보 주소는 고른 플랫폼 기준으로 만들어집니다. GitLab은 /oauth/authorize, Gitea는 /login/oauth/authorize를 씁니다. 잘못 고르면 존재하지 않는 경로로 갑니다.',
  gitShapeOidcHint:
    '자체 호스팅 GitLab / Gitea는 OIDC 신원 출처도 겸할 수 있습니다. 선언해야 로그인으로 얻은 토큰을 저장소 복사에 쓸 수 있고, 아래 "로그인 시 git 자격 증명 동기화"도 켤 수 있습니다. 확신이 없으면 비워 두고 신원 확인에만 사용하세요.',
  errGitShapeInvalid:
    '플랫폼 형태는 비워 두거나 gitlab 또는 gitea만 가능합니다. 그 외의 값을 넣으면 존재하지 않는 경로를 가리키는 인증 URL이 만들어집니다',

  // ─── 로그인 시 자격 증명 동기화(git 플랫폼 5종 + 형태를 선언한 OIDC의 로그인 카드만)──
  // 한 번의 로그인이 어디까지 가져가는지를 여기서 정합니다. 기본은 꺼짐이고,
  // 권한이 얼마로 오르는지, 만료되면 어떻게 회복하는지까지 같은 문안에 담습니다.
  syncTitle: '로그인 시 git 자격 증명 동기화',
  syncDesc: '이 플랫폼으로 로그인한 뒤 이번 승인으로 얻은 액세스 토큰을 이 계정의 개인 git 자격 증명으로 자동 저장합니다',
  syncAria: '{provider}의 로그인 시 동기화 켜기',
  syncNote:
    '켜면 로그인이 요청하는 권한이 "신원 확인만"에서 "저장소 읽기"로 올라갑니다. 만료 전에 플랫폼이 토큰을 자동 갱신하고, 갱신이 거부되면 해당 자격 증명에 "승인 만료" 표시가 붙으니 다시 로그인하면 회복됩니다. 동기화된 자격 증명은 이 계정에만 보이며, 프로젝트가 어떤 자격 증명을 쓸지는 계속 사용자가 고릅니다.',
  syncNeedsShapeHint:
    '이 OIDC 신원 출처는 자체 뒤에 있는 git 플랫폼을 아직 선언하지 않아 스위치가 눌려 있습니다. 위 "동시에 git 플랫폼"에서 고른 뒤에야 자격 증명을 동기화할 수 있습니다.',
  errSyncUnsupported:
    '이 신원 출처는 저장소를 복제할 수 있는 토큰을 발급하지 못합니다. 로그인 시 동기화는 git 플랫폼(Gitee / GitHub / GitLab / Gitea / 자체 호스팅)과 git 형태를 선언한 OIDC만 지원합니다',
  errSyncNeedsGitPlatform:
    '이 OIDC 신원 출처는 자체 뒤에 있는 git 플랫폼(GitLab / Gitea)이 선언되지 않아 "로그인 시 git 자격 증명 동기화"를 켤 수 없습니다. 먼저 "동시에 git 플랫폼"에서 하나를 선택하거나 이 스위치를 끄세요',

  // ─── OIDC 신원 출처(「플랫폼 로그인」에서만 나타남)──────────────────────
  // 저장할 때 서버가 엔드포인트를 실제로 해석합니다. 설정을 틀리면
  // "인증 화면은 통과했는데 돌아와서 오류"가 되므로 저장 시점에 실패시킵니다.
  providerOidcDesc: '범용 OIDC / SSO(Keycloak, Authentik 등)',
  oidcDiscoveryLabel: 'Discovery 주소',
  oidcDiscoveryOptional: '(아래 세 엔드포인트와 둘 중 하나)',
  oidcDiscoveryPlaceholder: 'https://sso.example.com/realms/my/.well-known/openid-configuration',
  oidcDiscoveryHint:
    '입력하면 인증 / 토큰 / 사용자 정보 엔드포인트를 문서에서 가져옵니다. 저장 시 실제로 불러오고, 실패하면 그 자리에서 오류를 알려줍니다.',
  oidcOverrideOptional: '(비우면 문서 값 사용)',
  oidcAuthorizeLabel: 'Authorization Endpoint',
  oidcTokenLabel: 'Token Endpoint',
  oidcUserinfoLabel: 'Userinfo Endpoint',
  oidcEndpointPlaceholder: 'https://sso.example.com/…',
  oidcEndpointsHint:
    '문서에 닿지 않으면(격리된 네트워크, 게이트웨이에 막힘) 세 개를 모두 직접 입력하세요. 직접 입력한 값이 우선합니다.',
  oidcIdClaimLabel: '기본 키 클레임',
  oidcIdClaimOptional: '(비우면 sub)',
  oidcIdClaimHint:
    '계정을 고유하게 식별하는 데 쓰는 클레임. 기본값은 sub(규약에 따라 바뀌면 안 됩니다). 이메일이나 로그인 이름으로 바꾸면 나중에 이름이나 이메일이 바뀐 사람이 남의 계정을 잡아타게 됩니다.',
  oidcScopesLabel: 'Scopes',
  oidcScopesOptional: '(비우면 openid profile email)',
  oidcScopesHint: '공백으로 구분. openid는 필수. 이메일을 받지 못하면 이메일 도메인 허용 목록이 로그인을 막습니다.',
  oidcUserinfoNote:
    '신원은 userinfo 엔드포인트에서 가져옵니다. 이 버전은 id_token 서명과 audience를 검증하지 않으므로 사내 네트워크나 HTTPS 공급자만 연결하세요.',
  errOidcEndpointsIncomplete:
    'OIDC를 활성화하려면 Discovery 주소를 입력하거나 인증 / 토큰 / 사용자 정보 세 엔드포인트를 모두 채우세요',
  errOidcConfigInvalid:
    '엔드포인트를 해석할 수 없습니다: 주소 오류, discovery 불러오기 실패, 또는 세 엔드포인트가 부족합니다(저장된 설정은 그대로입니다)',

  // ─── 딩톡 / 페이슈 / 기업위챗(「플랫폼 로그인」에만 나타남)─────────────────
  // 공식 엔드포인트는 서버 프리셋에 내장되어 있고, 설정 페이지에는 덮어쓸 항목만
  // 나옵니다. 내부 프록시나 자체 구축을 이용할 때만 채우세요.
  providerDingTalkDesc: '딩톡 QR 코드 로그인',
  providerFeishuDesc: '페이슈 QR 코드 로그인',
  providerWecomDesc: '기업위챗 QR 코드 로그인(직접 만든 앱)',
  clientIdPlaceholderPlatform:
    '오픈플랫폼 앱 상세의 Client ID(기업위챗은 기업 ID / corpid)',
  callbackHintPlatform:
    '오픈플랫폼 앱의 "callback / 리다이렉트 URL"에 이 주소를 입력하세요. 기업위챗은 이 인스턴스의 도메인을 "신뢰 가능한 도메인"에도 등록해야 합니다.',
  oidcScopesPlaceholder: 'openid profile email',
  platformOverrideOptional: '(비우면 플랫폼 기본 주소)',
  platformEndpointPlaceholder: 'https://…(내부 프록시·자체 구축에만 필요)',
  platformEndpointsHint:
    '인증 / 교환 / 신원 세 주소는 공식 값이 내장되어 있으니 보통은 비워 둡니다. 내부 프록시나 자체 구축을 이용할 때만 덮어쓰고, 입력한 값이 우선됩니다.',
  agentIdPlaceholder: '기업위챗 앱 상세의 AgentId(예: 1000002)',
  agentIdHint:
    'QR 인증 주소에 꼭 필요합니다(매개변수 이름은 agentid). Client ID 칸에는 AgentId가 아니라 기업 ID(corpid)를 넣습니다.',
  platformScopesOptional: '(비우면 기본값)',
  platformScopesPlaceholder: '공백 구분, 예: contact:user.base:readonly',
  platformScopesHint:
    '딩톡은 기본으로 openid, 페이슈는 기본으로 scope를 요청하지 않습니다(앱에 열리지 않은 권한을 요청하면 바로 거절됩니다). 메일 도메인 허용 목록으로 제한하려면 플랫폼이 메일 주소를 돌려주어야 합니다.',
  platformIdentityNote:
    '계정 조회는 플랫폼이 발급한 안정적 ID로만 합니다(딩톡 / 페이슈는 unionId, 없으면 openId. 기업위챗은 기업 ID + 구성원 userid). 별명이나 메일은 계정을 식별하지 않으니, 이름 변경이 다른 사람 계정으로 이어지지 않습니다.',
  errAgentIdRequired: '기업위챗은 AgentId가 필요합니다(앱 상세에 있음)',
  errPlatformConfigInvalid:
    '엔드포인트 덮어쓰기는 자격 증명을 포함하지 않은 http(s) 주소여야 하며, 비워 두면 플랫폼 기본 주소가 사용됩니다(저장된 설정은 그대로입니다)',
}

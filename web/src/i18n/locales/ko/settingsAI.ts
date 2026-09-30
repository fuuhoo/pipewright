export default {
  title: 'AI 제공자',
  subtitle:
    'Pipewright는 자체 모델을 학습하지 않습니다 —— 실패 진단과 설정 생성을 위해 직접 보유한 LLM을 연결하세요. 세 프로토콜이 각각 설정을 저장하며, 동시에 활성화되는 프로토콜은 하나입니다. 키는 이 인스턴스의 암호화된 보관소에만 저장되며 절대 외부로 유출되지 않습니다.',
  statusConfigured: '구성됨',
  statusUnconfigured: '미구성',
  activeBadge: '사용 중',
  retry: '다시 시도',

  providerClaudeTag: '진단 추천',
  providerOllamaDesc: '로컬 / 자체 호스팅',
  providerOllamaTag: '외부 전송 없음',
  protocolClaude: 'Claude 프로토콜',
  protocolOpenAI: 'OpenAI 프로토콜',
  protocolOllama: 'Ollama 프로토콜',
  protocolOpenAIDesc: 'GPT-4o, DeepSeek 등 호환 엔드포인트',

  guidanceAria: 'AI 구성 가이드',
  guidanceTitle: 'AI 진단을 활성화하려면 LLM을 구성하세요',
  guidanceBody:
    'Claude 프로토콜, OpenAI 프로토콜(DeepSeek 등 호환 엔드포인트) 또는 로컬 Ollama를 연결하면 파이프라인 실패 시 Pipewright가 근본 원인 가설과 수정 제안을 자동으로 생성하여 수동 로그 분석이 필요 없습니다.',

  selectProvider: '프로토콜 선택',
  providerRadioAria: 'AI 프로토콜 선택',
  selectProviderAria: '{name} 선택',
  providerConfig: '{name} 구성',
  lastSaved: '마지막 저장 {time}',

  apiKeyHint: '입력 후에는 마스킹된 값만 표시됩니다. 비워 두면 기존 키를 유지합니다',
  apiKeyReplacing: '교체 중…',
  apiKeyConfigured: '구성됨 ••••(비워 두면 변경 없음)',
  apiKeyPaste: 'API Key 붙여넣기…',
  apiKeyMaskedAria: '구성된 마스크: {masked}',

  ollamaHint: '로컬 Ollama는 API Key가 필요 없습니다 —— Ollama 서비스가 지정된 주소에서 실행 중인지 확인하세요.',

  baseUrlLabel: '접속 주소 (Base URL)',
  baseUrlHint: '기본값: {url}',
  presetLabel: '자주 쓰는 엔드포인트',
  presetApplyAria: '{name} 프리셋으로 엔드포인트와 모델 입력',

  modelLabel: '모델',
  modelHint: '진단에 사용하는 기본 모델, 예: claude-opus-4-7 / gpt-4o / deepseek-chat / llama3',

  testConnection: '연결 테스트',
  testOk: '연결 정상 · 지연 {ms}ms',
  testFail: '연결 실패',

  budgetLabel: '월 Token 한도',
  budgetHint: '초과 시 AI 진단을 일시 중지합니다(비워 두면 무제한. 이번 주기에는 선언만, 다음 Epic에서 강제 적용)',
  budgetPlaceholder: '예: 500000, 비워 두면 무제한',
  usedThisMonth: '이번 달 사용량 · 입력 {prompt} / 출력 {completion}',
  usedNone: '이번 달 사용 기록이 없습니다',

  enableAi: '이 프로토콜을 현재 사용으로 설정',
  enableAiDesc: '진단과 설정 생성은 이 프로토콜만 사용합니다. 활성화하면 나머지는 자동으로 비활성화됩니다',

  dirtyNote: '이 프로토콜에 저장하지 않은 변경 사항이 있습니다',
  cleanNote: '이 프로토콜은 변경 사항 없음',
  discard: '취소',
  saveChanges: '변경 사항 저장',

  toastSaveSuccess: '프로토콜 설정이 저장되었습니다',
  toastSaveFailed: '저장 실패',

  errServerUnreachable: '서버에 연결할 수 없습니다. 백엔드가 실행 중인지 확인한 후 다시 시도하세요.',
  errServerUnreachableShort: '서버에 연결할 수 없습니다',
  errVaultUnconfigured: '보관소에 master key가 구성되지 않았습니다. PIPEWRIGHT_MASTER_KEY 환경 변수를 설정하세요.',
  errLoadFailed: '불러오기 실패({status})',
  errLoadGeneric: 'AI 설정을 불러오지 못했습니다. 잠시 후 다시 시도하세요.',
  errBudgetInvalid: '월 token 한도는 양의 정수이거나 비워 두어야 합니다',
  errProviderInvalid: '유효한 프로토콜을 선택하세요',
  errBaseUrlRequired: '접속 주소를 입력하세요',
  errApiKeyRequired: 'API Key는 비워 둘 수 없습니다(Ollama 외 필수)',
  errRequestFailed: '요청 실패({status})',
  errUnknown: '알 수 없는 오류',
}

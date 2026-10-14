/**
 * '로컬 캐시' 카드(파이프라인 편집기 → 변수 및 캐시 탭).
 * 문구 원칙: 지우기는 안전한 동작입니다. 대가는 '다음 빌드가 한 번 느려진다'뿐, 파괴적인 작업처럼 쓰지 마세요.
 */
export default {
  title: '로컬 캐시',
  sub: '이 프로젝트가 빌드 호스트 디스크에 남기는 내용입니다. 지우면 다음 빌드에서 원격에서 다시 가져옵니다',
  kindRepo: '저장소 미러',
  kindDeps: '의존성 캐시',
  reload: '크기 재계산',
  reloadAria: '캐시 크기를 다시 계산합니다',
  loading: '크기 계산 중…',
  emptyNode: '이 컴퓨터에는 아직 이 프로젝트의 캐시가 없습니다.',
  total: '총 {size}',
  clearBtn: '캐시 지우기',
  clearing: '지우는 중…',
  clearTitle: '두 캐시를 모두 삭제해 다음 빌드를 깨끗한 상태부터 시작합니다',
  clearDisabledTitle: '현재 사용 중인 캐시가 없어 지울 필요가 없습니다',
  confirmTitle: '이 프로젝트의 로컬 캐시를 지우시겠습니까?',
  confirmLead: '빌드 호스트에서 다음을 삭제합니다:',
  confirmCost: '무엇도 손상되지 않습니다. 다음 빌드에서 저장소를 다시 클론하고 의존성을 다시 다운로드하므로 한 번만 느려집니다.',
  confirmShared: '저장소 미러는 저장소 URL 기준으로 저장되어, 같은 저장소를 쓰는 다른 프로젝트도 함께 삭제됩니다.',
  confirmBtn: '지우기',
  cleared: '{size} 확보',
  wasEmpty: '두 캐시 모두 비어 있어 지울 항목이 없습니다.',
  loadFailed: '캐시 크기 읽기에 실패했습니다',
  clearFailed: '캐시 지우기에 실패했습니다',
}

# 스냅샷 정규형 비교 버퍼 실험

현재 Go 생산 코드의 읽기 allocation profile을 먼저 수집하고, 표준 JSON Encoder의 borrowed bytes를 비교하는 후보를 같은 상태에서 대조한다. Python 3 표준 라이브러리와 프로젝트 Go wrapper, macOS arm64/APFS/cgo를 사용한다. [결과](benchmarks/snapshot-canonical-buffer.md)를 함께 읽는다.

## 명령과 순서

후보 생산 코드를 바꾸기 **전에** prepare를 실행한다. 입력은 배열 최적화 때 보존한 [두 상태·세 tail 묶음](snapshot-array-allocation.md)이며 그 candidate 생산 source와 현재 기준이 같아야 한다.

```sh
# 기준 생산 코드에서 allocation profile: 두 상태 각 5회, 총 10개 새 worker
python3 tools/diagnostics/canonical_buffer.py prepare \
  --bundle var/snapshot-array-resident-full-20261113 \
  --output var/canonical-buffer-new

# 최신 할당 비중 확인·후보 생산 코드 수정·회귀 검사 후
python3 tools/diagnostics/canonical_buffer.py freeze \
  --workspace var/canonical-buffer-new --label candidate
python3 tools/diagnostics/canonical_buffer.py compare \
  --workspace var/canonical-buffer-new

# 봉인·원 자료 검증과 새 폴더의 profile/JSON/Markdown 재집계
python3 tools/diagnostics/canonical_buffer.py analyze \
  --bundle var/canonical-buffer-new --output var/canonical-analysis-new
```

작은 smoke는 prepare에 `--smoke`와 기존 `var/snapshot-array-resident-smoke-20261113` 입력을 함께 쓴다. profile 2개, 비교 12개 worker다. full은 1만 명령 active/history·이후 명령 0/1천/5천·버전별 5회, 총 60개 비교 worker다. 새 clone에는 원본 var가 없으므로 보관 묶음을 복원하거나 앞선 도구로 기준 자료를 생성해야 한다.

prepare는 HEAD의 생산 코드만 허용한다. 현재 코드가 이미 후보로 바뀌었으면 중단하고, 원 기준 revision/source archive를 별도로 복원해 작업한다. baseline/candidate를 모두 실제 source archive·바이너리·Go 환경과 함께 동결하고 생산 차이 decode.go 하나·측정 Go probe 동일을 요구한다. 전체 workspace를 봉인하기 전 기준/공통 입력/profile 결과를 prepared.json의 파일별 SHA로 고정하며 비교 전 다시 확인한다.

## 범위와 메모리

최신 allocation profile은 tail=0, MemProfileRate=65536에서 공개 Open을 각 5회 수집한다. 전후 profile 차이를 pprof로 합치고 whole Open/읽기/decode focus를 나눈다. 값은 표본 추정 할당이며 유지 heap/RSS나 wall 시간이 아니다. focus는 서로 중첩되고 json.Marshal 누적에는 bytes.Clone이 포함되므로 합하지 않는다. 별도로 정확한 API TotalAlloc counter도 보존한다. CPU profiler는 켜지 않는다.

이후 60회 비교는 profile·IO observer 없는 새 프로세스에서 공개 Open의 wall·TotalAlloc·load/restore/replay/confirm·GC 뒤 live heap/현재 Mach resident_size·Open 직후까지의 getrusage peak RSS를 기록한다. 정확한 snapshot/재생 수·fallback 부재·최초 응답 8개/같은 ID 재시도 2개·후속 체결/취소·잔고/예약금·최종 canonical snapshot bytes/전체 WAL 불변을 매 worker에서 검사한다. 기존 engine/checkpoint 복구 oracle과 독립 정수 기대값을 쓰며 독립 매칭 엔진 비교는 아니다.

GOMAXPROCS=CPU 수/GOGC=100/GOMEMLIMIT=2GiB, 값 16KiB/깊이64·읽기32KiB·payload2GiB/배열 제한을 유지한다. GOMEMLIMIT는 Go soft limit이며 측정 검증기는 peak RSS·GC 뒤 heap/현재 RSS가 각각 2GiB 이하여야 통과시킨다. snapshot 파일 상한이 전체 상태 메모리 가용성을 보장하지 않는 기존 계약도 유지한다.

정규형 검증은 typed Unmarshal 뒤 표준 Encoder의 compact/default escaping 출력과 원문을 완전히 비교한다. 비교 writer는 Encoder의 framing 개행 하나를 별도로 확인하고 전달된 bytes를 복사/보관하지 않는다. 원문 고정 버퍼는 Encode가 끝나면 참조를 해제한다. canonical/UTF-8·정수/null/필드·오류·두 checksum·복구/WAL 대조·strict sync를 생략하지 않는다. 표준 라이브러리의 내부 인코딩 작업 버퍼/정수 codec 할당까지 없앴다는 뜻은 아니다.

## 공간·보존·재집계

처음 새 workspace를 만들 때 128MiB 준비 여유와 8GiB 별도 여유를 요구한다. profile과 비교가 공통 fixture 사본을 공유하고, 매 복사/빌드/실행마다 실제 추가량과 4GiB 묶음 예산·8GiB 여유를 검사한다. 기준/후보 빌드는 초기 workspace 안에서 단계적으로 생성하므로 전체 fixture를 매 단계 중복 보관하지 않는다. 분석만 새 폴더에 만들 때는 8MiB 추가 예산과 8GiB 여유를 확인한다.

성공한 새 작업 사본만 소유 marker·정확한 파일 집합·SHA/size 확인 후 verified-files.json을 남기고 정리한다. profile 사본은 원래 statescale marker 소유권부터 확인한 뒤 공통 정리 함수로 넘긴다. 원본·공통 fixture·고정 빌드·실패/예상 밖 파일은 보존하며, 부분 실패의 재시도는 기존 결과를 덮어쓰지 않는다.

analyze는 봉인과 prepared 파일 hash·두 빌드/전체 source archive·공통 입력·모든 결과/정리 receipt를 검증한다. 원 profile로 함수표를 새로 만들고 기존 profile-summary 및 JSON/Markdown의 동일성을 확인한다. 원 입력 경로를 읽거나 보관한 probe 바이너리를 실행하지 않는다. 구현 완료 뒤 source가 바뀌더라도 동결 바이너리/Go pprof를 사용하며 toolchain은 같아야 한다.

warm-cache·비격리 개발 호스트이며 유휴 gate/cache flush는 없다. 느린 표본을 제외/교체하지 않는다. HTTP p99·최대 지속 처리량·cold-cache/디스크 지연의 인과 효과·실제 OS/전원 장애 검증은 범위 밖이다. 실제 전원 장애는 **미완료·보류**다.

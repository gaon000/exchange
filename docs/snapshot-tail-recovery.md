# 스냅샷 이후 명령의 복구 검증·비용 측정

총 10,000개 고유 명령과 최종 상태를 고정하고 snapshot 이후 명령이 0·1,000·5,000개인 조건을 active/history 각각 5회 측정한다. 선택 snapshot 순번은 10,000·9,000·5,000이다. [실제 결과](benchmarks/snapshot-tail-recovery.md)와 [JSON](benchmarks/20261113-snapshot-tail-recovery.json)을 함께 읽는다.

## 실행과 재집계

저장소 루트에서 macOS arm64/APFS/cgo·프로젝트 Go wrapper·Python 3 표준 라이브러리를 사용한다. 외부 dependency는 없다. sysctl로 CPU/메모리 metadata를 읽는다.

```sh
# full: 6조건 × 5회, 30개 새 worker
python3 tools/diagnostics/snapshot_tail.py run --output var/snapshot-tail-new

# 기능 smoke: 24명령, 이후 0·8·12개, 조건별 1회
python3 tools/diagnostics/snapshot_tail.py run --smoke --output var/snapshot-tail-smoke-new

# 원본 봉인 검증 뒤 새 폴더에 JSON/Markdown 재생성
python3 tools/diagnostics/snapshot_tail.py analyze --bundle var/snapshot-tail-new --output var/snapshot-tail-reanalysis-new
```

출력 parent가 있어야 하고 기존 출력/symlink를 재사용하지 않는다. 기존 입력 없이 현재 checkout에서 두 fixture와 세 snapshot을 직접 만든다. 생산 `internal`·`cmd`·Go 설정은 HEAD와 같아야 한다. 새 미추적 파일과 ignore된 생산 파일도 거절하며, 도구 변경은 source archive와 SHA로 동결한다. 원본/바이너리/소스는 실행 중 다시 확인한다. 실패·느린 표본을 보존한다. 준비 공간 128MiB, 전체 묶음 4GiB 상한과 별도 여유 8GiB를 검사하며 다른 원본을 삭제하지 않는다.

## 같은 최종 상태와 후속 거래

상태 유형마다 하나의 runId·초기 자금·전체 WAL을 공유한다. 모든 cut은 같은 WAL의 durable batch 경계다. worker에는 Genesis·전체 WAL·lock와 선택한 prefix snapshot 하나만 복사한다. `.tail-owned` 표식이 있는 새 사본만 측정한다. 반복마다 조건 순서를 회전하고 짝수 반복은 역순으로 실행한다.

명령열은 다음과 같다.

1. 첫 두 명령은 alice의 매수 `carry-fill`(9,990 ticks·2 lots), `carry-cancel`(9,980 ticks·1 lot)이다. 5,000/9,000 snapshot에서 두 주문이 OPEN이어야 한다.
2. 중간 9,992개 명령은 active에서 9,900 매수/10,100 매도를 교대로 쌓는다. history에서는 10,000 매도 2 lots→매수 1 lot→매도 잔량 취소→체결된 매수 취소 no-op를 반복한다. history의 양수 tail cut은 중간 부분 체결 주문도 남기므로 이후 취소가 재생된다.
3. 마지막 6개는 bob의 9,990 매도 1 lot 체결→`carry-fill` 잔량 취소→`carry-cancel` 취소→체결된 taker 취소→이미 취소된 주문 재취소→없는 주문 취소다. 첫 두 주문에 대한 체결·예약 반환은 두 양수 tail 조건 모두 snapshot 이후에 발생한다. tail 0에서는 이미 반영된 최종 snapshot을 복구한다.

최종 active는 주문 9,995/활성 9,992/체결 주문 1/취소 주문 2/체결 1건, history는 주문 4,999/활성 0/체결 주문 2,499/취소 주문 2,500/체결 2,499건이다. 최초 결과 캐시는 모두 10,000개다. 기존 단순 비용 fixture와 명령열이 다르므로 과거 수치와 직접 전후 성능 비교하지 않는다.

## 복구 후 확인

각 worker는 공개 `runtime.Open`을 사용하고 정확한 snapshot 순번/파일·재생 개수·fallback 부재를 요구한다. HTTP 계층은 사용하지 않는다. Open 뒤 재시도보다 먼저 `Read`로 다음을 검사한다.

- 최초 두 주문과 마지막 여섯 명령의 최초 응답 8개가 준비 때 보존한 결과와 같아야 한다. 처음 OPEN 응답은 현재 주문이 CANCELLED여도 그대로 OPEN이어야 한다.
- 현재 세 주문의 체결량·취소량·상태와 9,990 maker 가격/수량/양쪽 주문 참조를 대조한다. 두 계정 잔고는 별도 산술 기대식과 비교해 예약금 해제·정산을 확인한다.
- 최초 두 명령을 같은 ID/내용으로 재시도해 같은 응답을 받고, as-of seq=10,000과 WAL bytes가 바뀌지 않아야 한다. 최대 고유 명령 한도에서도 기존 결과가 반환되어야 한다.
- 최종 snapshot을 생성해 준비한 전체 canonical 최종 파일 SHA와 비교한다. 주문·호가 복구에 필요한 상태·체결/입력/최초 결과 전체가 같은 bytes여야 한다. 선택 prefix snapshot·Genesis·WAL·lock도 변경되지 않아야 한다.

전체 상태의 기준은 같은 엔진으로 순차 실행한 fixture다. 별도 잔고/상태/가격 기대값을 추가했지만 거래 엔진 전체의 독립 구현과 비교한 검증은 아니다.

## 계측·보존 범위

새 worker마다 Open wall·TotalAlloc 증가·GC 뒤 살아 있는 heap/증가·Open 직후까지의 누적 peak RSS와 recovery load/restore/replay/confirm 단계를 기록한다. replay 단계는 prefix 검사와 suffix 적용을 함께 포함한다. **tail 0도 전체 WAL prefix를 검사**한다. Confirm은 선택 prefix snapshot의 canonical 확인/동기화이며 최종 상태의 새 snapshot 생성이 아니다.

준비/사본/해시/강제 GC/후속 조회·재시도·검증용 최종 publication/종료는 Open wall 밖이다. Open 내부 자연 GC와 I/O 대기는 포함한다. RSS는 프로세스 시작 이후 누적 high-water이고 API 구간만의 독립 peak가 아니다. 전체 wall은 저장소 열기/검사도 포함하므로 네 단계 중앙값 합과 같지 않다.

`protocol.json`은 조건/순서, `build.json`·`source.tar.gz`·`statescale`은 실행한 소스/바이너리, `fixtures`는 공통 WAL/세 snapshot/최초 응답, `runs`는 각 사본/원시 관측/로그를 보존한다. `report.json`/`report.md`와 manifest/completed hash를 봉인한다. analyze는 원본 검증 후 같은 보고서를 새 폴더에만 쓴다.

GOMAXPROCS는 호스트 논리 CPU 수, GOGC=100/GOMEMLIMIT=2GiB다. 해시/복사 뒤 warm-cache이며 CPU 유휴 gate·cache flush·profile을 사용하지 않은 개발 호스트 관찰이다. cold-cache·동시 HTTP 지연/최대 처리량·OS 종료/전원 장애 실험으로 해석하지 않는다. 실제 전원 장애는 **미완료·보류**다.

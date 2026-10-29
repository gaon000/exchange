# v1 저장 형식과 플랫폼 계약

상태: Accepted (P0 구현 기준)
결정일: 2026-10-29 (Asia/Seoul)
구현 상태: P2의 Genesis·WAL·복구 확정 API와 공유 내구성 어댑터를 구현했다. P3 runtime에 저장→적용→응답과 재생을 연결했다. P4는 상태 DTO·직접 복원·스트리밍 코덱·파일 게시와 runtime의 실행 중 생성·종료 합류·기동 시 후보 선택·fallback·WAL 대조까지 구현했다.

[상세 설계](exchange-v1.md)의 저장 후 적용·결정적 재생 계약을 구체화한다. 단위·명령·응답은 [API 계약](api-v1.md)을 따른다. v1의 검증 대상은 macOS arm64의 로컬 APFS이며 다른 OS·네트워크 파일 시스템으로 보장을 확대하지 않는다.

## 1. 데이터 디렉터리와 초기화

[서버 CLI](../../cmd/exchange/command.go)는 `bin/exchange init --config config/examples/local.json --data-dir var/runs/run-1`과 `bin/exchange serve --data-dir var/runs/run-1`을 제공한다. 빌드와 parent 준비는 [개발 기준](../development.md#cli-초기화실행종료)을 따른다. 초기화와 시작을 별도 명령으로 유지하며 serve에는 초기 설정을 덮어쓰는 옵션이 없다.

```text
run-1/
  .lock
  genesis.bin
  commands.wal
  snapshot-00000000000000001024.bin
```

- init은 기존 parent 아래 새 run 디렉터리를 단일 mkdir로 생성한다. 대상이 존재하면 비어 있어도 거절하고 덮어쓰거나 자동 초기화하지 않는다. parent는 미리 준비한다.
- 새 runId를 생성하고, 설정 검증·정규화 후 Genesis를 한 번 작성한다. 초기 잔고는 전부 available, reserved=0이다. 내부 오류나 저장 실패로 미완성 디렉터리가 남으면 그 사실을 보고하고 보존한다.
- `.lock`은 같은 inode를 유지한다. open 후 `flock(LOCK_EX|LOCK_NB)`을 획득한 프로세스만 파일을 읽어 복구하거나 기록한다. lock FD는 종료까지 보유하고, 잠금 파일을 unlink/재생성하지 않는다. PID 문자열만으로 소유권을 판정하지 않는다.
- v1은 단일 `commands.wal`을 append한다. 세그먼트 회전·WAL 삭제·결과 만료는 구현하지 않는다. 정리 범위를 줄여 먼저 복구를 검증한다.
- init 완료 전 Genesis와 WAL 헤더의 파일 동기화, run 디렉터리 동기화, run을 생성한 parent 디렉터리 동기화를 모두 확인한다. 일반 serve는 Genesis를 수정하거나 초기 잔고를 다시 지급하지 않는다.
- CLI는 설정·자원 한도를 먼저 검사하고 serve의 loopback 주소를 확보한 뒤 복구한다. 잘못된 옵션이나 이미 사용 중인 포트 때문에 실패한 기동은 WAL tail을 변경하지 않는다. HTTP 처리는 복구 검증·strict sync 완료 뒤 시작한다. 정상 종료는 HTTP 연결과 runtime 저장 작업을 함께 기다리며 HTTP 대기 제한이 저장 작업을 취소하지 않는다.
- 운영 파일/디렉터리 권한은 각각 0600/0700으로 시작한다. run 디렉터리는 서버만 변경하는 로컬 실험 경계다. 외부 프로세스가 파일을 교체하는 환경은 지원하지 않는다.

## 2. 버전과 canonical JSON

`formatVersion=1`, `rulesVersion=1`, `responseVersion=1`로 시작한다. format은 바이트 형식, rules는 매칭/잔고/거절 의미, response는 최초 응답 DTO다. 알 수 없는 버전이나 서로 다른 runId/configHash는 기동을 거절한다. 규칙 버전만 같게 붙이고 실행 의미를 변경하지 않는다.

본문은 UTF-8 compact JSON이다. JSON number는 버전과 소수 자릿수 같은 작은 메타데이터에만 쓰고, 금액·수량·순번·한도는 API와 같은 정규화 정수 문자열을 쓴다. 개행·불필요 공백 없음, 필드 순서는 아래 계약에 고정한다. map 순회로 canonical bytes를 만들지 않고 고정 struct와 정렬한 배열을 사용한다. 문자열은 Go encoding/json 기본 escaping을 사용하며 허용 ID/literal의 ASCII 범위를 검증한다.

- Config 필드 순서: [초기 설정 예시](../../config/examples/local.json)의 선언 순서. accounts는 accountId 바이트 사전순, 항목은 accountId/baseLots/quoteAtoms 순이다. 한도·단위의 v1 고정값도 포함한다.
- `configHash`: 정규화 Config bytes의 SHA-256, 소문자 64자리 hex. 입력 파일의 들여쓰기·계정 배열 순서는 hash에 영향을 주지 않는다. 잘못된 중복 계정은 정규화 전에 거절한다.
- Genesis payload 필드: runId, configHash, config. `config`에는 초기 잔고·전체 한도·모든 규칙 버전이 포함된다.
- WAL metadata payload 필드: runId, configHash, rulesVersion, responseVersion. 이후 모든 command record는 이 식별 정보를 상속한다.
- Command payload 필드: commandSeq, kind, accountId, payload. kind는 PLACE_ORDER 또는 CANCEL_ORDER이고 payload의 필드 순서는 API의 각 필수 본문 표 순서다. 헤더의 runId와 commandSeq까지 포함한 입력으로 최초 결과를 결정한다.
- 응답에 시각을 넣지 않으므로 v1 WAL에 wall clock은 없다. tradeId는 runId/commandSeq/matchIndex로 결정한다. 해시·JSON 파서 버전 변경은 golden bytes 테스트로 검증한다.

## 3. Genesis·WAL 헤더·snapshot envelope

아래 정수 바이트 필드는 모두 little-endian이다. 길이 계산은 overflow 검사 후 수행하며 선언 길이만 보고 무제한 메모리를 할당하지 않는다.

| offset | 길이 | 값 |
| --- | ---: | --- |
| 0 | 8 | ASCII magic: Genesis `EXGEN001`, WAL header `EXWAL001`, snapshot `EXSNP001` |
| 8 | 4 | uint32 formatVersion=1 |
| 12 | 8 | uint64 payloadBytes |
| 20 | payloadBytes | canonical JSON payload |
| 20+payloadBytes | 32 | 앞의 20+payloadBytes 전체의 SHA-256 raw bytes |

Genesis payload 최대 4 MiB, WAL metadata 최대 4 KiB, snapshot payload 최대 2 GiB다. Genesis/snapshot 파일은 정확히 envelope 하나이며 남는 바이트도 오류다. WAL은 envelope 하나 뒤에 아래의 완전한 그룹들이 연결된다. P4의 [snapshot.Write/Read](../../internal/snapshot/codec.go)는 streaming으로 쓰고 검사하며 선언 길이만큼 선할당하지 않는다. 상한을 넘으면 출력 전에 ErrTooLarge를 반환한다. 이 파일 상한은 전체 상태 사본의 가용 메모리를 보장하지 않는다.

Write는 payload를 먼저 세고, 32 KiB 버퍼와 SHA-256을 거쳐 같은 bytes를 기록한다. Read는 전체 envelope 길이·SHA-256을 검증한 뒤 [항목별 decoder](../../internal/snapshot/decode.go)를 사용한다. 계정·주문·체결·결과 한 항목은 최대 16 KiB이며, 배열과 가격별 FIFO는 항목 단위로 읽는다. 배열 수는 실제 읽은 항목과 계정 수 10,000/검증된 WAL 순번으로 제한한다. 상태를 담은 DTO 외에 거대한 JSON payload 사본을 만들지 않는다. 실제 decode bytes도 다시 hash하여 두 번의 읽기 사이 변경을 거절한다.

ErrCorrupt는 손상·비정규형 후보로 fallback 대상이다. 유효 checksum을 가진 지원 불가 format/rules/response 버전, 다른 runId/configHash 또는 WAL 끝보다 큰 snapshotSeq는 ErrIdentity로 복구를 중단한다. 실제 I/O 오류는 ErrCorrupt로 낮추지 않는다. 코덱은 업무 상태의 타당성을 보증하지 않으며 runtime의 복구 계층이 Restore와 WAL 대조를 수행한다.

Genesis와 WAL header는 서로 일치해야 한다. 비어 있는 WAL도 유효한 헤더는 필요하다. 완성되지 않았거나 checksum이 다른 Genesis/header를 정상 새 실험으로 취급하지 않는다.

## 4. WAL 그룹 프레임

그룹 헤더는 36 bytes다. 첫 그룹은 seq=1이고 이후 `firstSeq = 이전 firstSeq + 이전 count`다. Genesis는 commandSeq를 소비하지 않는다.

| offset | 길이 | 값 |
| --- | ---: | --- |
| 0 | 8 | ASCII `EXGRP001` |
| 8 | 4 | uint32 headerBytes=36 |
| 12 | 4 | uint32 payloadBytes, 1~1,048,576 |
| 16 | 8 | uint64 firstSeq, int64 양수 범위 |
| 24 | 4 | uint32 commandCount, 1~256 |
| 28 | 4 | uint32 flags=0 |
| 32 | 4 | CRC32C(header[0:32]), Go hash/crc32 Castagnoli |

payload는 `uint32 recordBytes + canonical command JSON`을 count회 이어 붙인다. recordBytes는 1~4,096이다. payloadBytes에는 각 4-byte 길이도 포함한다. record 내부 commandSeq는 firstSeq부터 정확하게 연속해야 한다. 남는 payload bytes, count/길이 불일치, 잘못된 canonical 입력은 손상이다.

footer는 16 bytes다: ASCII `EXEND001` 8 bytes, uint32 totalGroupBytes (`36+payloadBytes+16`), uint32 CRC32C. 마지막 CRC는 header + payload + footer 앞 12 bytes 전체를 대상으로 한다. checksum은 우발적 손상 검사용이며 악의적 파일 변조를 인증하지 않는다.

기본 배치 한도는 256명령/1 MiB payload/첫 명령부터 1ms 대기다. 먼저 찬 조건에서 닫고, 큰 단일 record가 한도에 맞지 않으면 형식 검사 단계에서 거절한다. 이 수치는 초기 설정이며 처리량이나 지연 달성의 증거가 아니다. 한 배치 sync·적용 후 다음 배치를 처리한다.

쓰기 순서는 header/payload/footer의 short write를 끝까지 처리 → 엄격한 동기화 성공 → durableSeq 전진 → 순차 상태 적용 → 결과 게시다. 어느 쓰기·동기화라도 실패하면 다음 명령 처리를 중단한다. footer는 과거 sync 완료를 증명하지 않는다.

## 5. snapshot DTO와 게시

snapshot payload 필드 순서는 runId, configHash, rulesVersion, responseVersion, snapshotSeq, state다. state는 다음을 고정 배열로 포함한다.

| state 필드 | 내용·순서 |
| --- | --- |
| accounts | accountId 순. ID와 자산별 available/reserved 정수 값 |
| orders | accountId/clientOrderId 순. 활성·종료·거절 Order DTO 모두 |
| book | bids/asks, 가격 우선 순. 가격별 clientOrderId+accountId FIFO 목록 |
| trades | commandSeq/matchIndex 순, 가격·수량·양쪽 주문/계정 참조 |
| results | accountId/kind/requestId 순. canonical 원래 command와 불변 최초 response |

상태 DTO의 필드 순서는 [domain.Checkpoint](../../internal/domain/checkpoint.go)의 선언과 [독립 Python 생성기](../../internal/engine/testdata/generate_checkpoint.py)의 [golden JSON](../../internal/engine/testdata/checkpoint-v1.json)으로 고정한다. [독립 envelope 생성기](../../internal/snapshot/testdata/generate.py)와 [golden snapshot](../../internal/snapshot/testdata/snapshot.bin)으로 바이트 형식을 검사한다.

- accounts 항목은 accountId, availableBaseLots, reservedBaseLots, availableQuoteAtoms, reservedQuoteAtoms다.
- orders 항목은 accountId, order다. order와 response는 API의 전체 Order/Result 형식을 재사용한다. reason은 미설정 시 null이며 빈 문자열이나 알 수 없는 문자열은 허용하지 않는다.
- book은 bids, asks를 갖는다. 각 level은 priceTicks, orders이고 orders의 참조는 accountId, clientOrderId다.
- trades 항목은 tradeId, commandSeq, matchIndex, maker, taker, takerSide, priceTicks, quantityLots다. maker/taker는 같은 주문 참조 형식이다.
- results 항목은 command, response다. command는 accountId, kind와 해당 종류의 place 또는 cancel 하나만 포함한다. runId는 envelope에서 공급하며 response에도 보존한다. 거절과 취소 no-op도 보존한다.
- 모든 배열은 빈 경우에도 `[]`다. 정수는 API와 같은 십진 문자열이며 JSON 필드명·필수 필드·중복 키 검사는 strictjson을 사용한다.

[engine.Capture/Restore](../../internal/engine/checkpoint.go)는 사본과 복원된 상태 사이의 가변 참조를 공유하지 않는다. Restore는 과거 명령을 다시 매칭하지 않고 인덱스와 FIFO를 직접 구성한다. 순번·주문·체결·최초 결과 관계, 예약액·총공급량·Genesis와 체결에서 계산한 계정별 보유량을 검증하고 모순된 후보를 거절한다. 특히 취소 이후 체결, 최초 응답 변경, 누락된 활성 주문을 허용하지 않는다. 파일 복구 계층은 여기에 더해 저장된 command 입력/순번을 보존된 WAL과 대조해야 한다. 이 상태 검증만으로 파일 게시·재시작 복구가 완료된 것은 아니다.

명령 경계 S에서 owner가 사본을 만든다. [snapshot.Publish](../../internal/snapshot/publish.go)는 S를 20자리 0-padding한 `snapshot-S.bin`으로 게시한다. 호출자는 run의 WAL 잠금을 보유하고 writer를 하나로 직렬화해야 한다. Publish 자체가 별도 잠금을 획득하거나 owner에서 사본을 만드는 것은 아니다.

P7에서 사본 생성과 정렬을 분리했다. `engine.CopyCheckpoint`는 owner 안에서 가변 참조를 모두 복사하되 accounts/orders/results의 map 순서를 그대로 둔다. runtime은 이 사본의 소유권을 한 snapshot worker에 넘기며 이후 사본을 읽거나 변경하지 않는다. worker가 `engine.CanonicalizeCheckpoint`로 세 배열을 정렬한 뒤 Publish에 불변 DTO를 전달한다. book의 가격/FIFO와 trades 순서는 정렬 대상이 아니다. 동기 호출용 `engine.Capture`는 복사와 정렬을 함께 수행하는 기존 계약을 유지한다. 파일 형식·정렬 기준은 변하지 않으며 Restore는 비정규 순서의 저장 상태를 정렬해서 고치지 않고 거절한다.

1. 입력·전체 payload 한도를 확인한다. 실패하면 파일 생성 전 종료한다.
2. APFS 확인 후 같은 디렉터리에 고유 `.snapshot-*.tmp`를 O_EXCL/0600으로 생성한다.
3. envelope 전체 쓰기 → 파일 F_FULLFSYNC → close → 정식 이름으로 exclusive rename → 디렉터리 F_FULLFSYNC 순으로 게시한다. 각 오류와 close 오류를 호출자에게 전달한다.
4. 성공한 뒤에만 caller가 snapshotSeq를 전진시킨다. rename 뒤 오류·프로세스 종료에는 정상 이름의 파일이 이미 존재할 수 있다. 오류를 성공으로 간주하지 않고 재기동 시 검증·재동기화해야 한다.

기존 같은 S의 파일은 canonical envelope 전체를 스트리밍 비교한다. 정확히 같으면 파일과 디렉터리를 다시 엄격한 sync하고, 다르거나 손상됐으면 ErrExistsMismatch로 거절한다. 확인과 rename 사이 파일이 나타난 경우에도 같은 규칙을 적용한다. 기존 파일을 지우거나 덮어쓰지 않는다. 실패 시 자신이 만든 임시 파일만 정리하며, 정리 오류는 원래 오류와 함께 반환한다. 프로세스 종료로 남은 임시 파일은 다음 복구에서 후보로 쓰지 않아야 한다.

Darwin [RenameExclusive](../../internal/durable/rename_darwin.go)는 [Apple rename 계약](https://raw.githubusercontent.com/apple-oss-distributions/xnu/main/bsd/man/man2/rename.2)의 `renamex_np(RENAME_EXCL)`을 호출한다. 대상이 있으면 EEXIST이며, 지원 불가 오류를 일반 rename으로 낮추지 않는다. 현재 macOS/APFS에서 새 이름 게시·기존 이름 보존과 strict sync를 검증했다. 다른 OS/cgo 비활성에서는 파일 생성 전에 지원 불가로 실패한다.

### 실행 중 생성과 종료

[Runtime.Snapshot(ctx)](../../internal/runtime/snapshot.go)은 Go 제어 API다. HTTP endpoint는 없으며 P6 CLI의 선택적인 주기 worker가 이 API를 호출한다. 응답은 RunID와 실제로 게시한 SnapshotSeq다.

- 접수·복사·정렬·게시를 합쳐 동시에 한 요청만 유지한다. 추가 요청은 `SNAPSHOT_BUSY/NOT_ADMITTED`로 즉시 거절한다. 응답 대기는 runtime의 전역 요청 한도에도 포함된다.
- owner가 처리 중 명령의 적용을 끝낸 뒤 사본을 만든다. 현재 구현은 한 WAL 배치 적용 중에도 사본을 만들지 않는다. 아직 큐·배치·WAL 동기화에 있는 명령을 모두 포함한다는 보장은 없으며 생성 요청 자체는 명령 큐의 barrier가 아니다.
- 사본 생성 동안에는 owner 처리가 멈춘다. 사본을 전달한 뒤 별도 snapshot worker가 정렬·저장하는 동안 주문·조회·WAL worker는 진행할 수 있다. 파일 크기 제한은 사본 할당 전의 메모리 상한이 아니다.
- context 취소는 응답 대기만 끝내고 `SNAPSHOT_TIMEOUT/UNKNOWN`을 반환한다. 이미 접수한 생성은 계속하고, 응답 대기 슬롯을 해제해도 다음 사본은 기존 게시가 끝날 때까지 허용하지 않는다.
- 파일과 디렉터리의 strict sync 성공을 owner가 확인한 뒤에만 `Runtime.SnapshotSeq()`를 전진시키고 성공을 응답한다. owner는 자신의 WAL 완료 확인 순번과 appliedSeq를 사용하며 쓰기 중인 wal.Store를 동시 조회하지 않는다.
- 파일 접근 전 순수 payload 상한 거절인 `snapshot.ErrTooLarge`만 생성 실패로 반환하고 거래를 계속한다. 실제 I/O·게시 오류·다른 bytes의 기존 파일·worker panic은 접수를 닫고 실패 상태로 전환한다. ErrTooLarge와 함께 반환된 I/O/정리 오류도 중단 대상이다.
- Shutdown은 이미 접수한 생성과 명령·조회를 처리하고 두 저장 worker의 종료를 기다린 뒤 WAL 핸들과 run 잠금을 닫는다. 정렬 중이거나 실패한 경우에도 진행 중 worker가 끝나기 전 잠금을 풀지 않는다. 실패 후 worker가 게시한 파일은 메모리 snapshotSeq에 반영하지 않고 다음 기동에서 검사한다.

`SnapshotSeq()`는 이번 runtime이 복구에서 선택했거나 게시 완료를 확인한 순번이다. 0은 Genesis 경계이므로 초기 snapshot 파일 유무는 `Recovery().SnapshotFile`로 구분한다. timeout·오류·응답 전 종료만으로 파일이 없다고 판단하지 않는다.

## 6. 복구 판정

잠금 → Genesis/WAL header 확인 → 전체 WAL 프레임/연속성 검사 → 정상 snapshot 선택 → 후속 command 재생 → 불변식 검사 → 남길 WAL 및 변경 메타데이터 엄격한 sync → ready 순이다. v1은 WAL을 전부 보존하므로 snapshotSeq 이전 그룹도 구조와 checksum을 검사한다. snapshotSeq는 실제 유효 WAL 끝을 넘을 수 없다.

[runtime.Open의 복구 계층](../../internal/runtime/recovery.go)은 다음과 같이 동작한다.

1. wal.Open이 잠금과 전체 WAL 검증을 마친 뒤 [snapshot.List/Load](../../internal/snapshot/files.go)를 호출한다. 정식 20자리 이름을 순번 내림차순으로 검사하며 `.snapshot-*.tmp`는 후보가 아니다. `snapshot-*.bin`이지만 이름이 비정규형이면 조용히 무시하지 않고 중단한다.
2. 검토하는 후보의 envelope·identity·파일명/본문 순번을 확인한 뒤 engine.Restore로 업무 상태를 직접 구성한다. 비정규 파일·내용 손상·상태 불변식 오류는 후보를 보존하고 이전 후보로 넘어간다. 읽기·닫기 I/O 오류와 실험/버전 불일치는 중단한다.
3. 전체 WAL을 다시 순서대로 읽는다. snapshotSeq 이하 명령은 최초 결과의 원래 입력·순번과 대조하고, 이후 명령만 Apply한다. 후보 불일치는 visitor 밖에 보관하여 정상 WAL 핸들을 실패 상태로 만들지 않는다. 동시에 발견한 WAL 오류는 fallback보다 우선하여 중단한다.
4. 최종 순번·불변식이 맞으면 선택한 snapshot의 bytes를 재확인하고 파일·디렉터리를 엄격한 sync한다. 이어 WAL ConfirmRecovery가 허용된 tail 절단과 WAL/Genesis/잠금/디렉터리 동기화를 마친 뒤 readiness를 연다. 선택한 파일이 사라지면 자동 재생성하지 않는다.
5. `Runtime.Recovery()`는 선택 파일·snapshotSeq·실제로 Apply한 후속 명령 수·건너뛴 후보의 진단을 사본으로 제공한다. 정상 후보가 없으면 Genesis부터 재생한다. seq=0 snapshot과 snapshot 부재는 파일명 유무로 구분한다. 정상 후보가 선택되면 더 오래된 snapshot은 읽지 않는다.

- 다음 그룹의 36-byte header를 물리 EOF 때문에 끝까지 읽지 못하면 마지막 미완성 그룹이다.
- header 전체를 읽었는데 magic/길이/CRC/seq/버전이 틀리면 EOF 근처여도 손상으로 기동 중단한다.
- 정상 header가 가리킨 payload/footer가 물리 EOF로 잘렸을 때만 해당 마지막 그룹을 통째로 버릴 수 있다. 그 뒤의 magic을 검색해 재동기화하지 않는다.
- 완전한 그룹의 checksum 오류·record 파싱 오류·순번 누락은 중단한다. 길이 필드가 비정상인데 단순 tail로 분류하지 않는다.
- 잘린 꼬리는 마지막 완전한 그룹 끝으로 truncate하고 WAL을 엄격한 sync한다. 완전한 미응답 그룹은 재생 후에도 반드시 sync하여 이번 복구에서 내구성을 확정한다.
- 최신 snapshot이 손상되면 이전 정상 snapshot 또는 Genesis를 쓰되 전체 WAL이 유효해야 한다. runId/configHash가 다르면 조용히 다른 실험 파일로 대체하지 않고 중단한다.
- 디스크/외부 행위가 이미 성공 응답한 로그 전체를 삭제하거나 정상 경계까지 잘라낸 경우를 checksum만으로 탐지할 수 없다. 이는 저장장치 정상·단일 소유자 전제 밖이며 별도 복제나 외부 commit watermark는 v1 범위 밖이다.

## 7. macOS 내구성 어댑터

Go 1.27.3의 [File.Sync 구현](https://go.dev/src/internal/poll/fd_fsync_darwin.go)은 F_FULLFSYNC를 먼저 호출하지만 ENOTSUP에서는 fsync로 fallback한다. [Apple 설명](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fsync.2.html)의 일반 fsync와 drive-cache flush 차이를 고려해 v1에서는 이 fallback을 허용하지 않는다.

P2의 Darwin 어댑터는 cgo로 시스템 `fcntl(fd, F_FULLFSYNC)`를 직접 호출한다. EINTR만 재시도하고 ENOTSUP·EINVAL·EIO 등은 오류로 전달한다. Go 파일의 SyscallConn.Control 안에서 FD를 사용해 닫힘과의 경합을 피한다. cgo와 Apple Command Line Tools는 개발 toolchain이며 추가 Go production module은 필요하지 않다. 다른 OS/cgo 비활성화는 명시적인 지원 불가다.

파일 내용뿐 아니라 생성·rename 후 관련 디렉터리, 새 run 생성 후 parent도 같은 strict sync 경로를 사용한다. lock FD는 종료까지 유지한다. 프로세스 강제 종료는 OS cache를 비우지 않으므로 전원 장애 검증을 대신하지 않는다.

P0의 [storageprobe](../../tools/storageprobe/main.go)는 실제 APFS에서 별도 임시 디렉터리를 만들고 파일 sync, rename+directory sync, 잠금 충돌, truncate+sync, read-back을 확인했다. 테스트는 독립 프로세스 간 잠금과 close 후 해제, 닫힌 파일 sync 거절, 기존 parent 파일 보존도 확인한다. 대상은 호출 성공과 오류 전달이며 전원 차단·WAL 재생은 미검증이다.

### P2 구현과 호출 계약

[wal.Store](../../internal/wal/store.go)는 자체 고루틴·mutex 없이 한 소유자가 호출한다. 업무 상태는 소유하지 않는다. [durable](../../internal/durable/darwin.go)은 probe와 WAL이 함께 쓰는 strict sync·flock 경계이며 지원되지 않는 플랫폼은 파일을 만들기 전에 실패한다.

- `DecodeConfig`는 byte 상한·엄격한 JSON·단위/공급량을 검사한다. `Init`은 UUID v4를 생성하고 계정 정렬·configHash를 포함한 Genesis를 영구 기록한다. 기존 디렉터리는 재사용하지 않는다.
- `Open`은 잠금 후 Genesis와 WAL 전체를 읽고 완전한 그룹의 순번·등록 계정·요청 키 유일성까지 확인한다. 파일은 수정하지 않는다. 검증한 끝은 `Recovery()`이며 아직 `DurableSeq()`는 0이다.
- 호출자는 새 엔진에 `Replay`하고 매 명령의 appliedSeq와 불변식을 확인한다. 오류가 나면 부분 복원한 엔진을 버리고 저장소를 닫는다. 조회·결과를 아직 공개하지 않는다.
- `ConfirmRecovery`만 허용된 잘린 꼬리를 truncate하고 남길 WAL, Genesis, lock inode, run/parent 디렉터리를 엄격히 동기화한다. 모두 성공해야 durableSeq를 확인된 끝으로 전진시키고 Append를 연다. 완전한 미응답 그룹도 이 경로를 거친다.
- `Append`는 연속적이고 중복 키가 없는 한 그룹을 기록·동기화한다. 기록 전 입력 오류는 파일을 바꾸지 않는다. write/sync 실패는 저장소를 실패 상태로 고정하며 후속 쓰기를 거절한다. engine 적용·응답은 성공 이후 호출자 책임이다.
- `Close`는 FD를 닫아 잠금을 해제하며 `.lock`을 삭제하지 않는다. 초기화/복구 실패에서도 기존 파일을 보존한다. 외부 파일 교체는 지원 경계 밖이며, 열린 파일이 아닌 경로의 symlink/비정규 파일은 거절한다.

P2는 [독립 Python 인코더](../../internal/wal/testdata/generate.py)의 golden bytes, 메모리 I/O 오류 주입, 실제 APFS 파일과 별도 프로세스 잠금을 구분해 검사했다. 이 테스트에서 재생 결과는 P1 엔진의 주문·잔고·최초 결과와 일치하지만 아직 runtime 응답 경로·snapshot·전원 장애 검증은 아니다. 실행 증거는 [구현 계획](../exec-plans/active/20261029-1531-exchange-v1.md)에 기록한다.

## 8. 검증 단계

- P2: envelope/group golden bytes, 모든 바이트 절단, 길이/체크섬 손상, short write/EINTR/sync 오류, 명령 크기/정수 한도, 두 프로세스 잠금, 파일·parent sync 누락 검출.
- P3: sync 실패 시 미적용, 완전한 미응답 그룹 재생, 응답 유실 후 최초 결과 재현.
- P4: 모든 게시 지점 종료, 최신 snapshot 실패 시 fallback, snapshotSeq 앞/뒤 검증, 전체 WAL 재생과 상태/결과 동일성.
- P5: 폐기 가능한 별도 환경의 실제 OS/전원 장애 검증. P0 probe 성공으로 이 완료 조건을 해제하지 않는다.

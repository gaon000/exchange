# 동시 시나리오 다양화·반복 실행

기존 [동시 요청 검사](concurrent-recovery.md)에 세 v3 profile과 제한된 seed/반복 캠페인을 추가했다. 매 실행은 새 거래 저장소이며 실제 HTTP·WAL 순서·snapshot/정상 재기동·최초 결과·같은 ID 중복 방지를 검사한다. 지연/처리량 성능 실험이나 SIGKILL·전원 장애 실험은 아니다. SIGKILL은 [별도 실행기](concurrent-crash-recovery.md)를 사용한다.

## 명령

```sh
# 기본: 세 profile × seed17/18 × 각2회 = 독립 run12개
sh scripts/random-recovery.sh --campaign --seed 17 --output var/campaign-example

# 한 구성만 3회, 자료 예산은 바이트
sh scripts/random-recovery.sh --campaign --profile id-conflict \
  --seed 42 --seed-count 1 --repeats 3 --duration 2m \
  --disk-budget 67108864 --output var/campaign-conflicts

# 단일 v3 시나리오
sh scripts/random-recovery.sh --concurrent --profile cancel-race --seed 17

# 관측된 실제 저장 순서로 단일 실행을 새 폴더에 재현
sh scripts/random-recovery.sh --replay var/campaign-example/case-000-cancel-race/replay-005.json
```

기본 output은 새 `var/scenarios/<UTC 시각>`이다. 기존 출력/원본 안쪽·symlink parent 보호는 기존 실행기와 같다. 캠페인은 replay/actions/transport-loss/concurrent flag와 함께 지정하지 않는다. 단일 --concurrent에는 profile만 추가할 수 있다. 기존 profile 없는 --concurrent는 v2, seed/transport-loss는 v1으로 유지한다. 새 profile은 v3이므로 기존 명세의 의미를 바꾸지 않는다.

| 옵션 | 제한/기본 |
| --- | --- |
| `--profile` | 캠페인 기본은 all. cancel-race / duplicate-storm / id-conflict 하나 선택 가능 |
| `--seed`, `--seed-count` | 시작 seed0~2147483647, 연속 seed1~4개. 마지막 seed도 범위 안이어야 함. 기본17/2개 |
| `--repeats` | 같은 seed/profile을 새 run에서1~3회. 기본2회 |
| 전체 실행 수 | profile 수 × seed 수 × 반복 수, 최대24개 |
| `--duration` | 실행기 내부 캠페인 시간 예산10초~5분, 기본2분. toolchain 준비/빌드 시간은 제외 |
| `--disk-budget` | 해당 새 캠페인 폴더의 일반 파일 논리 bytes 합, 16~256MiB. 기본64MiB. 캐시/빌드·다른 var 자료는 포함하지 않음 |

## 세 구성과 저장 계약

각 profile은 준비 명령·동시 initial/retry 두 wave·마지막 정상 재시작으로 구성한다. seed가 주문 수량/후속 계정·가격과 client ID 역할 배치를 정하며 OS 도착 순서는 정하지 않는다. phase별 요청 수는4/12/8이고 client ID는0부터 연속된다. 두 phase 모두 모든 중계에 도착한 뒤 공통 gate를 연다.

| profile | phase 클라이언트 | 추가로 확인하는 경쟁/실패 |
| --- | ---: | --- |
| cancel-race | 4 | 신규 대기 매수의 생성과 그 주문을 취소하는 요청이 경쟁. 취소가 먼저면 저장된 ORDER_NOT_FOUND가 이후 주문 생성/재시도 뒤에도 최초 결과로 유지됨. 주문 이후 취소는 해당 시점 상태에 따라 처리 |
| duplicate-storm | 12 | 같은 매수 ID의 초기10복사본과 재시도8복사본. 유실한 복사본과 완전한 응답을 받은 복사본이 같은 키에 공존해도 각 client의 UNKNOWN을 유지 |
| id-conflict | 8 | 이미 완료된 준비 주문/취소 ID에 다른 내용을 병행 제출. 주문 수량·취소 대상의 다른 내용은409 IDEMPOTENCY_CONFLICT/NOT_ADMITTED이며 canonical 최초 결과·WAL은 불변 |

각 wave의 응답 헤더 유실·본문 절단·held-original과 snapshot/tail·늦은 원래 요청 검사는 유지한다. v3 attempt의 expectConflict=true는 **이미 완료된 키의 다른 내용**에만 허용하고, 응답 유실이나 snapshot witness와 결합하지 않는다. 새 키의 동시 다른 내용 중 어느 쪽이 이길지 미리 가정하는 검사가 아니다. 충돌은 canonical 입력을 덮어쓰거나 새 WAL/중복 성공으로 세지 않는다. 409의 NOT_ADMITTED는 해당 다른 내용 시도만 뜻하며 원래 canonical 명령의 접수/완료를 부정하지 않는다.

WAL 재생과 전체 checkpoint/공급량·잔고/예약금·호가/FIFO·체결·최초 결과 검사는 기존 경로를 사용한다. 같은 ID 중복 건수는 두 phase 전체 시도에서 새 고유 WAL 명령과 의도한 충돌을 뺀 값이다. 두 wave 합계는 cancel-race8, duplicate-storm40, id-conflict8이며 id-conflict의 의도한409는 별도16회다. 새 고유 명령은 각각12/12/14개다. 서로 다른 유효 저장 순서에서는 결과가 달라질 수 있다. 실제 관측 순서를 기록 replay로 재현하면 동일한 논리 결과와 경계를 확인한다.

## 시간·용량 중단과 실패 보존

캠페인은 첫 실패에서 다음 실행을 시작하지 않는다. 매 실행 전에8MiB의 보수적 공간과 최종 metadata1MiB를 예산 안에서 예약한다. 실행 중20ms 간격으로 논리 bytes를 확인해 임계 초과/계측 오류이면 context를 취소한다. 정상적인 snapshot 임시 파일 rename 중 사라진 경로는 집계에서 건너뛴다. deadline/SIGINT/SIGTERM도 진행 중 실행의 취소/종료 정리를 요청한다. 모든 실행이 끝난 뒤 봉인 전 시간·용량을 다시 확인한다.

이 예산은 파일 시스템 quota가 아니다. 감시 간격과 이미 진행 중인 저장·실패 기록/정리 때문에 초과를 관측한 실패 폴더는 예산보다 클 수 있다. 실패 원본을 삭제해 예산 아래인 것처럼 만들지 않는다. 최종 성공 폴더는 budget 안이어야 하며 summary의 observedBytesBeforeMetadata는 최종 봉인 파일을 쓰기 전 관측값이다. 필요한 예약 공간이 없으면 실제 크기가 아직 budget 미만이어도 다음 실행을 시작하지 않는다.

실행 전에 case-NNN-input.json에 전체 입력을 저장한다. 개별 run이 출력 생성 전에 실패해도 이 파일로 한 명령 재현이 가능하다. 관측 순서를 얻었으면 해당 run의 마지막 유효 replay를 우선한다. failed.json의 각 results[].reproduce와 개별 failed.json/원본을 사용한다. 환경/미관측 interleaving·디스크 오류 자체의 동일 발생은 보장하지 않는다. 예산 부족으로 시작하지 않은 다음 case는 campaign.json의 seed/profile로 따로 실행할 수 있다. 자동 재시도·기존 run 재사용·삭제는 하지 않는다.

성공은 top-level completed.json과 manifest 봉인이 모두 있어야 한다. 실패 전에 일부 case가 통과했거나 summary가 기록됐더라도 캠페인 전체 성공으로 표시하지 않는다. 합성 예산 회귀는 실제 거래 증거와 구분하며 실제3profile과 기록 replay는 native 회귀/실측으로 확인한다.

| 파일 | 내용 |
| --- | --- |
| `campaign.json` | 옵션·정해진 case 순서·seed/profile/반복과 예약 예산 |
| `case-NNN-input.json`, `case-NNN-result.json` | 실행 전 전체 명세와 성공/실패·관측 카운터/재현 명령 |
| `case-NNN-<profile>/` | 기존 실행기와 동일한 완전한 run·관측/WAL/checkpoint·replay·개별 봉인 또는 실패 |
| `summary.json`, `report.md` | 성공 캠페인의 case별 JSON/Markdown 표, 실패 시에는 failed.json의 부분 results 사용 |
| `transport-spec.json` | 각 성공 run을 선택하는 읽기 전용 분석 보고서 명세 |
| `manifest.json`, `completed.json`, `failed.json` | 하위 run을 포함한 전체 파일 봉인 또는 중단/실패·다음 case·보존된 결과 |

## 분석·회귀

```sh
python3 tools/diagnostics/experiment.py transport-report \
  --spec var/campaign-example/transport-spec.json --output var/campaign-analysis-new

scripts/go test -race ./tools/recoverycheck -count=1 -timeout=180s
bash scripts/ci.sh portable
bash scripts/ci.sh native
```

[분석 보고서](concurrent-evidence-report.md)는 v3 profile/4~12클라이언트·의도한 충돌을 구분한다. conflictObservations와 충돌 표에 phase/client·ID·409/NOT_ADMITTED·지연을 기록하고 원래 UNKNOWN/조회/retry를 바꾸지 않는다. 저장되지 않을 충돌 입력은 canonical 계획/WAL 키 집합에서 제외해 실제 고유 순서와 checkpoint를 대조한다. v1/v2/SIGKILL 입력도 계속 지원한다.

portable은 결정적인 profile/명세·budget/flag와 합성 실행기를 통한 용량 예약/초과·취소·출력/시작 전 실패 보존을, native는 실제3profile·관측 replay·첫 wave의 두 취소 저장 순서와 최초 결과를 검사한다. native 보고서 smoke는 실제 짧은 캠페인 자료를 읽는다. 원본/실패는 CI의 기존 random-recovery-* 경로에 보존된다.

[실제 캠페인 결과](benchmarks/concurrent-campaign.md)를 함께 읽는다. 서버와 같은 매칭 구현을 비교 모델로 사용하며 독립 매칭 oracle·현재 최대 처리량·전원 장애 내구성을 새로 주장하지 않는다. 실제 OS·전원 장애 검증은 계속 미완료·보류다.

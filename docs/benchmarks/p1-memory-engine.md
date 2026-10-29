# P1 메모리 엔진 기준 측정

측정일: 2026-10-29 (Asia/Seoul)
범위: engine.Apply 직접 호출. 네트워크·JSON 요청 처리·큐·WAL·파일 동기화·snapshot·p99는 포함하지 않는다.

## 재현 조건

- Go 1.27.3 darwin/arm64, macOS 15.8, Apple M3 Pro, 11 logical CPUs, RAM 18 GiB.
- branch `codex/p1-memory-engine`, 기준 HEAD `5a10cf3` + 이 변경의 P1 소스. 당시 아직 미커밋 상태였으므로 HEAD만으로 재현할 수 없다.
- 측정 소스: [benchmark_test.go](../../internal/engine/benchmark_test.go). `internal/**/*.go`를 경로순 정렬하고 각 `경로 UTF-8 + NUL + 파일 bytes + NUL`을 연결한 SHA-256: `34b38590416b09079125e90368bd34244d54a7ddc86663ef4e0da7b85bae6965`.
- 테스트·race·fuzz·coverage 실행이 끝난 뒤 별도 프로세스로 측정했다. 이 실행에는 해당 계측이나 profiler를 켜지 않았다. 시스템 전체의 다른 사용자 프로세스 부하는 통제하지 않았다.
- 계정 a/b 각각 base 1,000,000 lots, quote 1,000,000,000 atoms. 가격 100, 수량 1, 가격 level 하나다. 여러 가격·깊은 호가·대규모 계정·오래 누적한 기록의 성능을 대표하지 않는다.

```sh
scripts/go test ./internal/engine -run '^$' -bench '^BenchmarkEngine$' -benchmem -benchtime=200ms -count=3
```

## 작업별 의미와 결과

| 작업 | 1 op의 의미 | 3회 ns/op 범위 | 중앙값 ns/op | B/op | allocs/op |
| --- | --- | ---: | ---: | ---: | ---: |
| Rest | 대기 매수 1건 추가 | 460.8~670.1 | 514.6 | 1,313 | 5 |
| Match | 매도 등록 + 다른 계정 매수 체결 | 1,163~1,232 | 1,206 | 3,036 | 13 |
| Cancel | 매수 등록 + 취소 | 850.5~861.1 | 859.7 | 2,257 | 9 |
| Duplicate | 이미 처리한 주문의 최초 결과 반환 | 148.8~154.2 | 149.5 | 144 | 1 |

Match/Cancel은 op당 신규 명령 2개, Rest는 1개, Duplicate는 0개다. Duplicate를 신규 주문 처리량으로 합산하지 않는다. 지표는 Go benchmark의 op 평균 비용이며 지연 분포나 p99가 아니다.

Rest/Match/Cancel은 기록이 무한히 쌓이지 않도록 512 ops마다 새 엔진으로 바꾼다. 최초 준비는 타이머 밖이고 이후 교체 비용은 측정에 포함한다. 이 정책은 테스트 프로세스 안에서 명시적으로 새 엔진을 만드는 것이며 실제 서버의 재시작·초기 잔고 재지급을 구현한 것이 아니다. ID 문자열은 미리 준비한다. 전체 State 복사·CheckInvariants는 hot path에서 제외했다.

[측정 원본](20261029-p1-memory-engine.txt)을 함께 보관한다. 이 값은 P7 자료구조·할당 비교의 출발점이며 P6에서 정의할 영구 저장 포함 지속 처리량 목표의 달성 근거로 사용하지 않는다.

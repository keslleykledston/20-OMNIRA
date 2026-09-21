#!/usr/bin/env python3
"""log_outcome.py — registra o que REALMENTE aconteceu para uma task_id já
roteada por route.sh, completando a linha correspondente no log jsonl.

Em ROUTER_MODE=shadow, route.sh só recomenda; quem executa (humano/agente)
continua escolhendo o modelo/tool livremente. Este script fecha o loop de
avaliação: grava qual tier foi realmente usado, tokens, custo, latência e o
resultado de teste, para comparar depois com route_recommended.

Uso:
  python3 log_outcome.py --task-id TASK-123 --actual-model claude-opus-5 \
    --tokens 4200 --cost-usd 0.31 --latency-ms 8400 --test-result pass

Não sobrescreve a linha original — acrescenta uma nova linha "outcome" com o
mesmo task_id, para manter o log append-only (auditável).
"""
import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

DEFAULT_LOG = Path(__file__).parent / "logs" / "decisions.jsonl"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--task-id", required=True)
    parser.add_argument("--actual-tier", required=True, choices=["TOOL", "LOCAL_LLM", "CHEAP_LLM", "FRONTIER_LLM", "LOVABLE", "HUMAN"])
    parser.add_argument("--actual-model", default=None)
    parser.add_argument("--tokens", type=int, default=None)
    parser.add_argument("--cost-usd", type=float, default=None)
    parser.add_argument("--latency-ms", type=int, default=None)
    parser.add_argument("--test-result", default=None, choices=["pass", "fail", "n/a", None])
    parser.add_argument("--log-file", default=str(DEFAULT_LOG))
    args = parser.parse_args()

    entry = {
        "ts": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "task_id": args.task_id,
        "kind": "outcome",
        "actual_tier": args.actual_tier,
        "actual_model_used": args.actual_model,
        "tokens": args.tokens,
        "cost_usd": args.cost_usd,
        "latency_ms": args.latency_ms,
        "test_result": args.test_result,
    }

    log_path = Path(args.log_file)
    log_path.parent.mkdir(parents=True, exist_ok=True)
    with open(log_path, "a", encoding="utf-8") as f:
        f.write(json.dumps(entry, ensure_ascii=False) + "\n")

    print(json.dumps(entry, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"log_outcome error: {exc}", file=sys.stderr)
        sys.exit(1)

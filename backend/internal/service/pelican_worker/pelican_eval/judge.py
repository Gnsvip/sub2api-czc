"""LLM Judge：多模态大模型给渲染画面打分，支持 Chat Completions / Messages / Responses 三种协议。"""

from __future__ import annotations

import base64
import json
import os
import statistics
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field

import anthropic
import openai

from .prompts import (
    CHECKLIST,
    CRITERIA_WEIGHTS,
    JUDGE_SYSTEM_PROMPT,
    JUDGE_USER_PROMPT,
    JUDGE_VERSION,
    SCORE_CAPS,
    SCORE_KEYS,
)

PROTOCOLS = ("chat_completions", "messages", "responses")
MAX_ATTEMPTS = 3  # 评委输出不符合格式时重问的次数


class JudgeConfigError(Exception):
    pass


@dataclass
class JudgeConfig:
    protocol: str
    base_url: str
    model: str
    api_key: str = field(repr=False)
    samples: int

    @classmethod
    def from_env(cls) -> JudgeConfig:
        missing = [k for k in ("JUDGE_BASE_URL", "JUDGE_MODEL", "JUDGE_API_KEY") if not os.getenv(k)]
        if missing:
            raise JudgeConfigError(f"缺少 {', '.join(missing)}")
        protocol = os.getenv("JUDGE_PROTOCOL") or "chat_completions"
        if protocol not in PROTOCOLS:
            raise JudgeConfigError(f"JUDGE_PROTOCOL 须为 {' / '.join(PROTOCOLS)}")
        samples = os.getenv("JUDGE_SAMPLES") or "3"
        if not samples.isdigit() or int(samples) < 1:
            raise JudgeConfigError("JUDGE_SAMPLES 须为正整数")
        return cls(protocol, os.environ["JUDGE_BASE_URL"], os.environ["JUDGE_MODEL"],
                   os.environ["JUDGE_API_KEY"], int(samples))

    def describe(self) -> dict:
        """写入 meta 的评委信息（不含 API key）。"""
        return {"protocol": self.protocol, "base_url": self.base_url, "model": self.model,
                "samples": self.samples, "rubric": JUDGE_VERSION}


class Judge:
    def __init__(self, config: JudgeConfig):
        self.config = config
        client = anthropic.Anthropic if config.protocol == "messages" else openai.OpenAI
        self.client = client(api_key=config.api_key, base_url=config.base_url)

    def evaluate(self, first_frame: bytes, contact_sheet: bytes, animated: bool) -> dict:
        """同一组画面独立评 samples 次后取平均；SVG 里没有动画时 animation_quality 记 0。"""
        images = [base64.b64encode(png).decode() for png in (first_frame, contact_sheet)]
        with ThreadPoolExecutor(self.config.samples) as pool:
            samples = list(pool.map(lambda _: self._sample(images, animated), range(self.config.samples)))
        return aggregate(samples)

    def _sample(self, images: list[str], animated: bool) -> dict:
        for _ in range(MAX_ATTEMPTS):
            try:
                return parse_judgement(self._ask(images), animated)
            except ValueError as exc:
                error = exc
        raise RuntimeError(f"invalid judge output: {error}")

    def _ask(self, images: list[str]) -> str:
        cfg = self.config
        urls = [f"data:image/png;base64,{image}" for image in images]
        if cfg.protocol == "chat_completions":
            resp = self.client.chat.completions.create(
                model=cfg.model,
                messages=[
                    {"role": "system", "content": JUDGE_SYSTEM_PROMPT},
                    {"role": "user", "content": [
                        {"type": "text", "text": JUDGE_USER_PROMPT},
                        *({"type": "image_url", "image_url": {"url": url}} for url in urls),
                    ]},
                ],
            )
            return resp.choices[0].message.content or ""
        if cfg.protocol == "responses":
            resp = self.client.responses.create(
                model=cfg.model,
                instructions=JUDGE_SYSTEM_PROMPT,
                input=[{"role": "user", "content": [
                    {"type": "input_text", "text": JUDGE_USER_PROMPT},
                    *({"type": "input_image", "image_url": url} for url in urls),
                ]}],
            )
            return resp.output_text
        resp = self.client.messages.create(
            model=cfg.model,
            max_tokens=16384,
            system=JUDGE_SYSTEM_PROMPT,
            messages=[{"role": "user", "content": [
                *({"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": image}}
                  for image in images),
                {"type": "text", "text": JUDGE_USER_PROMPT},
            ]}],
        )
        return "".join(block.text for block in resp.content if block.type == "text")


def parse_judgement(text: str, animated: bool) -> dict:
    """解析评委 JSON 并校验字段；核对项不通过时按 SCORE_CAPS 给相关维度封顶。"""
    try:
        data = json.loads(text[text.find("{"):text.rfind("}") + 1])
    except json.JSONDecodeError as exc:
        raise ValueError(f"malformed JSON: {exc}") from None
    checklist = data.get("checklist") or {}
    if not all(isinstance(checklist.get(k), bool) for k in CHECKLIST):
        raise ValueError("checklist must give true/false for every item")
    scores = {}
    for key in SCORE_KEYS:
        item = (data.get("scores") or {}).get(key)
        score = item.get("score") if isinstance(item, dict) else None
        if isinstance(score, bool) or not isinstance(score, (int, float)) or not 0 <= score <= 5:
            raise ValueError(f"invalid score for {key}")
        scores[key] = {"score": float(score), "reason": str(item.get("reason", ""))}

    caps = [(key, cap, f"{item}=false")
            for item, limits in SCORE_CAPS.items() if not checklist[item] for key, cap in limits.items()]
    if not animated:
        caps.append(("animation_quality", 0, "SVG has no animation"))
    caps_applied = []
    for key, cap, why in caps:
        if scores[key]["score"] > cap:
            caps_applied.append(f"{key}: {scores[key]['score']:g} -> {cap} ({why})")
            scores[key]["score"] = float(cap)

    criteria = {key: scores[key]["score"] for key in CRITERIA_WEIGHTS if key != "object_quality"}
    criteria["object_quality"] = (scores["pelican_quality"]["score"] + scores["bicycle_quality"]["score"]) / 2
    return {
        "observations": str(data.get("observations", "")),
        "checklist": {k: checklist[k] for k in CHECKLIST},
        "scores": scores,
        "caps_applied": caps_applied,
        "criteria": criteria,
        "overall": round(sum(w * criteria[k] / 5 for k, w in CRITERIA_WEIGHTS.items()) * 100, 1),
    }


def aggregate(samples: list[dict]) -> dict:
    overall = round(statistics.fmean(s["overall"] for s in samples), 1)
    rep = min(samples, key=lambda s: abs(s["overall"] - overall))  # 理由与观察取总分最接近平均分的那次
    reasons = {k: v["reason"] for k, v in rep["scores"].items()}
    reasons["object_quality"] = f"Pelican: {reasons['pelican_quality']} Bicycle: {reasons['bicycle_quality']}"
    return {
        "overall": overall,
        "scores": {
            k: {"score": round(statistics.fmean(s["criteria"][k] for s in samples), 2), "reason": reasons[k]}
            for k in CRITERIA_WEIGHTS
        },
        "checklist": {k: round(statistics.fmean(s["checklist"][k] for s in samples), 2) for k in CHECKLIST},
        "observations": rep["observations"],
        "samples": samples,
    }


def zero_result(reason: str) -> dict:
    """没有可评的画面（无 SVG、渲染失败）时直接记 0 分，不调用评委。"""
    return {"overall": 0.0, "scores": {k: {"score": 0.0, "reason": reason} for k in CRITERIA_WEIGHTS}}

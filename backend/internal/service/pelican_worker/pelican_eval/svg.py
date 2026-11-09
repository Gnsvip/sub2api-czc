"""SVG 提取、静态统计与按时间采样渲染。"""

from __future__ import annotations

import io
import math
import re
import xml.etree.ElementTree as ET
from collections import Counter
from dataclasses import dataclass

import resvg_py
from PIL import Image, ImageDraw, ImageFont

from .animation import SMIL_TAGS, AnimatedSVG, local

FENCE_RE = re.compile(r"```[^\n]*\n(.*?)```", re.S)


def extract_svg(text: str) -> str | None:
    """取最后一个含 <svg 的代码块（没有代码块时取全文）中从 <svg 到最后一个 </svg> 的部分。"""
    blocks = [block for block in FENCE_RE.findall(text) if "<svg" in block]
    chunk = blocks[-1] if blocks else text
    start, end = chunk.find("<svg"), chunk.rfind("</svg>")
    return chunk[start:end + len("</svg>")] if 0 <= start < end else None


def analyze_svg(svg: str) -> dict:
    """静态统计：path 数量、DOM 节点数量与动画定义数量。"""
    try:
        root = ET.fromstring(svg)
    except ET.ParseError as exc:
        return {"xml_valid": False, "xml_error": str(exc)}
    tags = Counter(local(el.tag) for el in root.iter())
    style = "".join("".join(el.itertext()) for el in root.iter() if local(el.tag) == "style")
    smil = sum(tags[tag] for tag in SMIL_TAGS)
    keyframes = len(re.findall(r"@(?:-webkit-)?keyframes\b", style))
    return {
        "xml_valid": True,
        "bytes": len(svg.encode("utf-8")),
        "dom_nodes": sum(tags.values()),
        "path_count": tags["path"],
        "smil_animations": smil,
        "css_keyframes": keyframes,
        "animated": bool(smil or keyframes),
    }


def render_png(svg: str, size: int) -> Image.Image:
    """在白底上把静态 SVG 渲染成长边为 size 像素的图片。"""
    image = Image.open(io.BytesIO(resvg_py.svg_to_bytes(svg_string=svg, background="white", width=size)))
    if image.height > image.width:
        image = Image.open(io.BytesIO(resvg_py.svg_to_bytes(svg_string=svg, background="white", height=size)))
    return image.convert("RGB")


def png_bytes(image: Image.Image) -> bytes:
    buffer = io.BytesIO()
    image.save(buffer, format="PNG", optimize=True)
    return buffer.getvalue()


@dataclass
class Scene:
    first_frame: Image.Image    # t=0 的大图
    frames: list[Image.Image]   # 给评委看的采样帧
    times: list[float]
    contact_sheet: Image.Image
    gif: bytes | None           # 动画预览，无动画时为 None
    animations: int             # 识别到的动画数量（SMIL 元素 + CSS 动画）
    cycle: float                # GIF 时长（秒）


def render_scene(svg: str, frame_count: int = 6) -> Scene:
    """渲染首帧、给评委的帧序列拼图，以及 GIF 预览。"""
    animated = AnimatedSVG(svg)
    times = animated.sample_times(frame_count)
    frames = [render_png(animated.bake(t), 512) for t in times]
    gif = None
    if animated.cycle:
        n = min(96, max(2, round(animated.cycle * 12)))  # 12 fps，最多 96 帧
        images = [render_png(animated.bake(animated.cycle * k / n), 480) for k in range(n)]
        buffer = io.BytesIO()
        images[0].save(buffer, format="GIF", save_all=True, append_images=images[1:],
                       duration=round(animated.cycle * 1000 / n), loop=0)
        gif = buffer.getvalue()
    return Scene(render_png(animated.bake(0.0), 1024), frames, times, contact_sheet(frames, times),
                 gif, animated.animation_count, animated.cycle)


def contact_sheet(frames: list[Image.Image], times: list[float], columns: int = 3) -> Image.Image:
    """把采样帧拼成网格，每帧上方标注序号与时间。"""
    width, height = frames[0].size
    font = ImageFont.load_default(size=max(14, width // 24))
    label, gap = font.size + 12, 10
    rows = math.ceil(len(frames) / columns)
    sheet = Image.new("RGB", (columns * (width + gap) + gap, rows * (height + label + gap) + gap), "#3a3a3a")
    draw = ImageDraw.Draw(sheet)
    for i, (image, t) in enumerate(zip(frames, times)):
        x = gap + i % columns * (width + gap)
        y = gap + i // columns * (height + label + gap)
        draw.text((x + 4, y + 5), f"Frame {i + 1}   t = {t:.2f}s", fill="white", font=font)
        sheet.paste(image, (x, y + label))
    return sheet

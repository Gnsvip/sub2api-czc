"""SVG 动画采样：求出 SMIL 与 CSS 动画在时刻 t 的状态，写回成静态 SVG 后交给 resvg 渲染。

覆盖 LLM 生成 SVG 的常见写法：animate / animateTransform / animateMotion / set，
以及 CSS @keyframes（transform、opacity、颜色等属性）。
"""

from __future__ import annotations

import math
import re
import statistics
import xml.etree.ElementTree as ET
from dataclasses import dataclass

import svgelements as se
from PIL import ImageColor

XLINK_HREF = "{http://www.w3.org/1999/xlink}href"
SMIL_TAGS = {"animate", "animateTransform", "animateMotion", "set"}
MAX_CYCLE = 8.0  # 时间窗口上限（秒）
NUMBER = r"[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?"
NUMBER_RE = re.compile(NUMBER)
ANGLE_UNITS = {"deg": 1.0, "rad": 180 / math.pi, "grad": 0.9, "turn": 360.0}
# 既是 SVG 属性又是 CSS 属性：SMIL 写属性时同时写内联样式，否则会被 class 规则盖住
STYLE_PROPS = {"fill", "stroke", "opacity", "fill-opacity", "stroke-opacity", "stroke-width",
               "stroke-dasharray", "stroke-dashoffset", "stop-color", "stop-opacity", "visibility", "display"}
INITIAL_VALUES = {"opacity": "1", "fill-opacity": "1", "stroke-opacity": "1"}


def local(tag) -> str:
    return tag.rsplit("}", 1)[-1] if isinstance(tag, str) else ""


def fmt_num(value: float) -> str:
    text = f"{value:.4f}".rstrip("0").rstrip(".")
    return "0" if text in ("", "-0") else text


def num(value: str | None, default: float = 0.0) -> float:
    match = NUMBER_RE.search(value or "")
    return float(match[0]) if match else default


def clock(value: str | None) -> float | None:
    """'1.5s' / '200ms' / '2' 转成秒；'click'、'indefinite' 等返回 None。"""
    match = re.fullmatch(rf"\s*({NUMBER})\s*(ms|s)?\s*", value or "")
    return float(match[1]) / (1000 if match[2] == "ms" else 1) if match else None


# ---------------------------------------------------------------- easing

def cubic_bezier(x1: float, y1: float, x2: float, y2: float):
    def curve(t: float, a: float, b: float) -> float:
        return 3 * a * t * (1 - t) ** 2 + 3 * b * t * t * (1 - t) + t ** 3

    def ease(x: float) -> float:
        lo, hi = 0.0, 1.0
        for _ in range(30):
            mid = (lo + hi) / 2
            lo, hi = (mid, hi) if curve(mid, x1, x2) < x else (lo, mid)
        return curve((lo + hi) / 2, y1, y2)

    return ease


def steps(count: int, start: bool):
    return lambda x: min(1.0, math.floor(x * count + start) / count)


EASINGS = {
    "linear": lambda x: x,
    "ease": cubic_bezier(0.25, 0.1, 0.25, 1),
    "ease-in": cubic_bezier(0.42, 0, 1, 1),
    "ease-out": cubic_bezier(0, 0, 0.58, 1),
    "ease-in-out": cubic_bezier(0.42, 0, 0.58, 1),
    "step-start": steps(1, True),
    "step-end": steps(1, False),
}


def parse_easing(value: str):
    text = value.strip().lower()
    if text in EASINGS:
        return EASINGS[text]
    nums = [float(n) for n in NUMBER_RE.findall(text)]
    if text.startswith("cubic-bezier(") and len(nums) == 4:
        return cubic_bezier(*nums)
    if text.startswith("steps(") and nums and nums[0] >= 1:
        return steps(int(nums[0]), "start" in text)
    return EASINGS["ease"]


# ---------------------------------------------------------------- interpolation

def parse_color(value: str) -> tuple[int, int, int] | None:
    text = value.strip().lower()
    if not (text.startswith(("#", "rgb", "hsl")) or text.isalpha()):
        return None
    try:
        return ImageColor.getrgb(text)[:3]
    except ValueError:
        return None


def lerp_value(a: str, b: str, p: float) -> str:
    """颜色按 RGB 插值；结构相同的数字串（长度、坐标、path d）逐个数字插值；其余在中点切换。"""
    if p <= 0:
        return a
    if p >= 1:
        return b
    ca, cb = parse_color(a), parse_color(b)
    if ca and cb:
        return "#" + "".join(f"{round(x + (y - x) * p):02x}" for x, y in zip(ca, cb))
    na, nb = NUMBER_RE.findall(a), NUMBER_RE.findall(b)
    ta, tb = NUMBER_RE.split(a), NUMBER_RE.split(b)
    if (na and len(na) == len(nb) and not re.search(r"url\(|#", a + b)
            and [s.strip(" ,") for s in ta] == [s.strip(" ,") for s in tb]):
        out = ta[0]
        for x, y, tail in zip(na, nb, ta[1:]):
            out += fmt_num(float(x) + (float(y) - float(x)) * p) + tail
        return out
    return a if p < 0.5 else b


# ---------------------------------------------------------------- CSS transform

ZERO, ONE = (0.0, ""), (1.0, "")
IDENTITY = {"translate": [ZERO, ZERO], "rotate": [(0.0, "deg")], "scale": [ONE, ONE]}


def parse_transform(value: str) -> list[tuple[str, list[tuple[float, str]]]] | None:
    """CSS transform 规范化为 translate / rotate / scale 列表；3D 等其余函数忽略，参数无法解析时返回 None。"""
    functions = []
    for name, raw in re.findall(r"([\w-]+)\s*\(([^)]*)\)", value):
        args = []
        for token in re.split(r"[\s,]+", raw.strip().lower()):
            match = re.fullmatch(rf"({NUMBER})([a-z%]*)", token)
            if not match:
                return None
            n, unit = float(match[1]), match[2]
            args.append((n * ANGLE_UNITS[unit], "deg") if unit in ANGLE_UNITS else (n, unit))
        name = name.lower()
        second = args[1] if len(args) > 1 else None
        if name in ("translate", "translate3d"):
            functions.append(("translate", [args[0], second or ZERO]))
        elif name == "translatex":
            functions.append(("translate", [args[0], ZERO]))
        elif name == "translatey":
            functions.append(("translate", [ZERO, args[0]]))
        elif name in ("rotate", "rotatez"):
            functions.append(("rotate", [args[0]]))
        elif name in ("scale", "scale3d"):
            functions.append(("scale", [args[0], second or args[0]]))
        elif name == "scalex":
            functions.append(("scale", [args[0], ONE]))
        elif name == "scaley":
            functions.append(("scale", [ONE, args[0]]))
    return functions


def lerp_transform(a: str, b: str, p: float) -> str:
    fa, fb = parse_transform(a), parse_transform(b)
    if fa is None or fb is None:
        return a if p < 0.5 else b
    # 一方是另一方的前缀（含 none）时，短的一方用单位变换补齐后逐项插值
    names_a, names_b = [n for n, _ in fa], [n for n, _ in fb]
    if names_a == names_b[:len(fa)]:
        fa = fa + [(n, IDENTITY[n]) for n in names_b[len(fa):]]
    elif names_b == names_a[:len(fb)]:
        fb = fb + [(n, IDENTITY[n]) for n in names_a[len(fb):]]
    else:
        return a if p < 0.5 else b
    return " ".join(
        f"{name}(" + ", ".join(fmt_num(x + (y - x) * p) + (ub or ua) for (x, ua), (y, ub) in zip(xs, ys)) + ")"
        for (name, xs), (_, ys) in zip(fa, fb)
    ) or "none"


# ---------------------------------------------------------------- CSS parsing

def split_top(text: str, sep: str) -> list[str]:
    """按分隔符切分，忽略括号内的分隔符。"""
    parts, depth, current = [], 0, ""
    for char in text:
        depth += {"(": 1, ")": -1}.get(char, 0)
        if char == sep and depth == 0:
            parts.append(current)
            current = ""
        else:
            current += char
    return [p.strip() for p in [*parts, current] if p.strip()]


def parse_declarations(body: str) -> list[tuple[str, str]]:
    return [
        (prop.strip().lower(), re.sub(r"\s*!important$", "", value.strip()))
        for prop, _, value in (item.partition(":") for item in split_top(body, ";"))
        if value
    ]


def blocks(text: str):
    """依次产出顶层的 (前导, 块内容)。"""
    i = 0
    while (brace := text.find("{", i)) >= 0:
        depth = 0
        for end in range(brace, len(text)):
            depth += {"{": 1, "}": -1}.get(text[end], 0)
            if depth == 0:
                break
        yield text[i:brace].rsplit(";", 1)[-1].strip(), text[brace + 1:end]
        i = end + 1


def compile_selector(selector: str):
    """只支持 tag / #id / .class 组合与后代关系，含伪类、属性、兄弟选择器的规则忽略。"""
    if re.search(r"[:\[+~]", selector) or not selector.strip():
        return None
    compounds = []
    for part in selector.replace(">", " ").split():
        match = re.fullmatch(r"(\*|[\w-]+)?((?:[#.][\w-]+)*)", part)
        if not match:
            return None
        tag = match[1] if match[1] != "*" else None
        compounds.append((tag, re.findall(r"#([\w-]+)", match[2]), re.findall(r"\.([\w-]+)", match[2])))
    specificity = (sum(len(ids) for _, ids, _ in compounds),
                   sum(len(classes) for _, _, classes in compounds),
                   sum(1 for tag, _, _ in compounds if tag))
    return specificity, compounds


def parse_css(text: str, rules: list, keyframes: dict) -> None:
    """规则按 (优先级, 出现顺序, 选择器, 声明) 追加到 rules，@keyframes 存入 keyframes。"""
    for prelude, body in blocks(re.sub(r"/\*.*?\*/", "", text, flags=re.S)):
        at_rule = prelude.split()[0] if prelude.startswith("@") else ""
        if at_rule.endswith("keyframes"):
            keyframes[prelude.split()[-1].strip("'\"")] = parse_keyframes(body)
        elif at_rule == "@media" and "reduce" not in prelude:
            parse_css(body, rules, keyframes)  # 模拟默认环境，跳过 prefers-reduced-motion: reduce
        elif not at_rule:
            declarations = parse_declarations(body)
            for selector in prelude.split(","):
                if compiled := compile_selector(selector):
                    rules.append((compiled[0], len(rules), compiled[1], declarations))


def parse_keyframes(body: str) -> list[tuple[float, dict]]:
    frames: dict[float, dict] = {}
    for selector, declarations in blocks(body):
        for key in selector.split(","):
            key = key.strip().lower()
            offset = 0.0 if key == "from" else 1.0 if key == "to" else num(key) / 100
            frames.setdefault(offset, {}).update(parse_declarations(declarations))
    return sorted(frames.items())


def matches(compounds, el, parents) -> bool:
    def match(compound, node) -> bool:
        tag, ids, classes = compound
        node_classes = (node.get("class") or "").split()
        return ((not tag or local(node.tag) == tag) and all(node.get("id") == i for i in ids)
                and all(c in node_classes for c in classes))

    if not match(compounds[-1], el):
        return False
    node = parents.get(el)
    for compound in reversed(compounds[:-1]):
        while node is not None and not match(compound, node):
            node = parents.get(node)
        if node is None:
            return False
        node = parents.get(node)
    return True


# ---------------------------------------------------------------- CSS animations

ANIMATION_PROPS = {
    "animation-name": "name", "animation-duration": "duration", "animation-delay": "delay",
    "animation-timing-function": "easing", "animation-iteration-count": "count",
    "animation-direction": "direction", "animation-fill-mode": "fill",
}
ANIMATION_DEFAULTS = {"name": "none", "duration": "0s", "delay": "0s", "easing": "ease",
                      "count": "1", "direction": "normal", "fill": "none"}


def parse_shorthand(value: str) -> dict[str, list[str]]:
    lists: dict[str, list[str]] = {key: [] for key in ANIMATION_DEFAULTS}
    for item in split_top(value, ","):
        anim = dict(ANIMATION_DEFAULTS)
        times = 0
        for token in split_top(item, " "):
            low = token.lower()
            if re.fullmatch(rf"{NUMBER}m?s", low):
                anim["duration" if times == 0 else "delay"] = low
                times += 1
            elif low in EASINGS or low.startswith(("cubic-bezier(", "steps(")):
                anim["easing"] = low
            elif low == "infinite" or re.fullmatch(NUMBER, low):
                anim["count"] = low
            elif low in ("normal", "reverse", "alternate", "alternate-reverse"):
                anim["direction"] = low
            elif low in ("none", "forwards", "backwards", "both"):
                anim["fill"] = low
            elif low not in ("running", "paused"):
                anim["name"] = token.strip("'\"")
        for key, v in anim.items():
            lists[key].append(v)
    return lists


@dataclass
class CssAnimation:
    frames: list[tuple[float, dict]]
    duration: float
    delay: float
    easing: object
    count: float
    direction: str
    fill: str

    def progress(self, t: float) -> float | None:
        """时刻 t 在当前迭代中的进度（已按 direction 翻转）；不生效时返回 None。"""
        active = t - self.delay
        if active < 0:
            if self.fill not in ("backwards", "both"):
                return None
            iteration, p = 0, 0.0
        elif active >= self.duration * self.count:
            if self.fill not in ("forwards", "both"):
                return None
            frac = self.count % 1
            iteration, p = (math.floor(self.count), frac) if frac else (math.ceil(self.count) - 1, 1.0)
        else:
            iteration, p = int(active // self.duration), active % self.duration / self.duration
        reverse = {"reverse": True, "alternate": iteration % 2 == 1, "alternate-reverse": iteration % 2 == 0}
        return 1 - p if reverse.get(self.direction) else p


def css_animations(declarations: list[tuple[str, str]], keyframes: dict) -> list[CssAnimation]:
    lists: dict[str, list[str]] = {}
    for prop, value in declarations:
        if prop == "animation":
            lists = parse_shorthand(value)
        elif prop in ANIMATION_PROPS:
            lists[ANIMATION_PROPS[prop]] = split_top(value, ",")
    animations = []
    for i, name in enumerate(lists.get("name", [])):
        props = {}
        for key, default in ANIMATION_DEFAULTS.items():
            values = lists.get(key) or [default]
            props[key] = values[i % len(values)]  # 第 i 个动画取各列表的第 i 项，不足时循环
        duration = clock(props["duration"]) or 0.0
        name = name.strip("'\"")
        if name in keyframes and duration > 0:
            animations.append(CssAnimation(
                frames=keyframes[name],
                duration=duration,
                delay=clock(props["delay"]) or 0.0,
                easing=parse_easing(props["easing"]),
                count=math.inf if props["count"] == "infinite" else num(props["count"], 1),
                direction=props["direction"],
                fill=props["fill"],
            ))
    return animations


def keyframe_value(points: list[tuple[float, str, str | None]], p: float, easing, prop: str) -> str:
    for (o1, v1, e1), (o2, v2, _) in zip(points, points[1:]):
        if p <= o2:
            u = (p - o1) / (o2 - o1) if o2 > o1 else 1.0
            u = (parse_easing(e1) if e1 else easing)(min(max(u, 0.0), 1.0))
            return lerp_transform(v1, v2, u) if prop == "transform" else lerp_value(v1, v2, u)
    return points[-1][1]


@dataclass
class CssTarget:
    index: int
    props: dict                         # 层叠后的 CSS 声明
    animations: list[CssAnimation]

    def values_at(self, t: float, el) -> dict[str, str]:
        values = {"transform": self.props["transform"]} if "transform" in self.props else {}
        for anim in self.animations:
            p = anim.progress(t)
            if p is None:
                continue
            for prop in {k for _, decls in anim.frames for k in decls} - {"animation-timing-function"}:
                points = [(o, d[prop], d.get("animation-timing-function")) for o, d in anim.frames if prop in d]
                # 缺少 0% / 100% 关键帧时用元素本身的值补齐
                if prop == "transform":
                    base = self.props.get("transform", "none")
                else:
                    base = self.props.get(prop) or current_value(el, prop) or INITIAL_VALUES.get(prop, points[0][1])
                if points[0][0] > 0:
                    points.insert(0, (0.0, base, None))
                if points[-1][0] < 1:
                    points.append((1.0, base, None))
                values[prop] = keyframe_value(points, p, anim.easing, prop)
        return values


# ---------------------------------------------------------------- geometry

def transform_box(box, transform: str | None):
    if not box or not transform:
        return box
    m = se.Matrix(transform)
    corners = [(m.a * x + m.c * y + m.e, m.b * x + m.d * y + m.f) for x in box[0::2] for y in box[1::2]]
    return (min(x for x, _ in corners), min(y for _, y in corners),
            max(x for x, _ in corners), max(y for _, y in corners))


def element_bbox(el):
    """元素在自身坐标系下的几何包围盒 (x0, y0, x1, y1)，用于 transform-box: fill-box。"""
    tag, g = local(el.tag), el.get
    if tag == "path":
        return se.Path(g("d") or "").bbox()
    if tag == "circle":
        cx, cy, r = num(g("cx")), num(g("cy")), num(g("r"))
        return cx - r, cy - r, cx + r, cy + r
    if tag == "ellipse":
        cx, cy, rx, ry = num(g("cx")), num(g("cy")), num(g("rx")), num(g("ry"))
        return cx - rx, cy - ry, cx + rx, cy + ry
    if tag == "rect":
        x, y = num(g("x")), num(g("y"))
        return x, y, x + num(g("width")), y + num(g("height"))
    if tag == "line":
        return (min(num(g("x1")), num(g("x2"))), min(num(g("y1")), num(g("y2"))),
                max(num(g("x1")), num(g("x2"))), max(num(g("y1")), num(g("y2"))))
    if tag in ("polyline", "polygon"):
        pts = [float(n) for n in NUMBER_RE.findall(g("points") or "")]
        return (min(pts[0::2]), min(pts[1::2]), max(pts[0::2]), max(pts[1::2])) if len(pts) >= 2 else None
    if tag in ("g", "a"):
        boxes = [b for child in el if (b := transform_box(element_bbox(child), child.get("transform")))]
        if boxes:
            return (min(b[0] for b in boxes), min(b[1] for b in boxes),
                    max(b[2] for b in boxes), max(b[3] for b in boxes))
    return None


def flatten_path(d: str) -> list[tuple[float, float, float]]:
    """路径离散成 (x, y, 累计长度) 折线，供 animateMotion 按长度取点。"""
    points: list[tuple[float, float, float]] = []
    length = 0.0
    for segment in se.Path(d).segments():
        if isinstance(segment, se.Move):
            points.append((segment.end.x, segment.end.y, length))
            continue
        for k in range(1, 33):
            pt = segment.point(k / 32)
            if points:
                length += math.hypot(pt.x - points[-1][0], pt.y - points[-1][1])
            points.append((pt.x, pt.y, length))
    return points


def point_on(points, frac: float) -> tuple[float, float, float]:
    target = frac * points[-1][2]
    for (x1, y1, l1), (x2, y2, l2) in zip(points, points[1:]):
        if l2 >= target and l2 > l1:
            u = (target - l1) / (l2 - l1)
            return x1 + (x2 - x1) * u, y1 + (y2 - y1) * u, math.degrees(math.atan2(y2 - y1, x2 - x1))
    return points[-1][0], points[-1][1], 0.0


# ---------------------------------------------------------------- element values

def current_value(el, name: str) -> str | None:
    return dict(parse_declarations(el.get("style") or "")).get(name) or el.get(name)


def set_style(el, prop: str, value: str) -> None:
    styles = dict(parse_declarations(el.get("style") or ""))
    styles[prop] = value
    el.set("style", "; ".join(f"{k}: {v}" for k, v in styles.items()))


def set_value(el, name: str, value: str) -> None:
    el.set(name, value)
    if name in STYLE_PROPS:
        set_style(el, name, value)


# ---------------------------------------------------------------- SMIL

def smil_begin(anim) -> float | None:
    return clock((anim.get("begin") or "0s").split(";")[0])


def smil_progress(anim, t: float) -> float | None:
    """SMIL 动画在时刻 t 的进度（0–1）；未开始、或已结束且不 freeze 时返回 None。"""
    begin, dur = smil_begin(anim), clock(anim.get("dur"))
    if begin is None or t < begin:
        return None
    if not dur:
        return 1.0 if local(anim.tag) == "set" else None
    count = anim.get("repeatCount")
    active = math.inf if count == "indefinite" else dur * num(count, 1)
    elapsed = t - begin
    if elapsed < active:
        return elapsed % dur / dur
    if anim.get("fill") != "freeze":
        return None
    return active % dur / dur or 1.0


def interpolate_smil(anim, values: list[str], p: float) -> str:
    n = len(values)
    if n == 1:
        return values[0]
    mode = anim.get("calcMode", "linear")
    times = [float(x) for x in NUMBER_RE.findall(anim.get("keyTimes") or "")]
    if mode == "discrete":
        times = times if len(times) == n else [i / n for i in range(n)]
        return values[max(i for i, k in enumerate(times) if k <= p or i == 0)]
    times = times if len(times) == n else [i / (n - 1) for i in range(n)]
    i = max((i for i in range(n - 1) if times[i] <= p), default=0)
    span = times[i + 1] - times[i]
    u = min(max((p - times[i]) / span, 0.0), 1.0) if span > 0 else 1.0
    splines = [NUMBER_RE.findall(s) for s in (anim.get("keySplines") or "").split(";")]
    if mode == "spline" and i < len(splines) and len(splines[i]) == 4:
        u = cubic_bezier(*map(float, splines[i]))(u)
    return lerp_value(values[i], values[i + 1], u)


def apply_smil(anim, target, t: float, ids: dict) -> None:
    p = smil_progress(anim, t)
    tag, name = local(anim.tag), anim.get("attributeName")
    if p is None:
        return
    if tag == "animateMotion":
        apply_motion(anim, target, p, ids)
    elif tag == "set":
        if name and anim.get("to") is not None:
            set_value(target, name, anim.get("to"))
    elif name:
        kind = anim.get("type", "translate") if tag == "animateTransform" else None
        if anim.get("values"):
            values = [v.strip() for v in anim.get("values").split(";") if v.strip()]
        else:
            start = anim.get("from") or ({"scale": "1"}.get(kind, "0") if kind else current_value(target, name))
            values = [v for v in (start, anim.get("to")) if v is not None]
        if not values:
            return
        value = interpolate_smil(anim, values, p)
        if kind:
            prefix = target.get("transform", "") if anim.get("additive") == "sum" else ""
            target.set("transform", f"{prefix} {kind}({value})".strip())
        else:
            set_value(target, name, value)


def apply_motion(anim, target, p: float, ids: dict) -> None:
    d = anim.get("path")
    for mpath in (c for c in anim if local(c.tag) == "mpath"):
        ref = ids.get((mpath.get("href") or mpath.get(XLINK_HREF) or "").lstrip("#"))
        d = ref.get("d") if ref is not None else d
    points = flatten_path(d) if d else []
    if len(points) < 2 or points[-1][2] <= 0:
        return
    x, y, angle = point_on(points, p)
    rotate = anim.get("rotate", "0")
    angle = angle if rotate == "auto" else angle + 180 if rotate == "auto-reverse" else num(rotate)
    motion = f"translate({fmt_num(x)} {fmt_num(y)}) rotate({fmt_num(angle)})"
    target.set("transform", f"{motion} {target.get('transform', '')}".strip())


# ---------------------------------------------------------------- baking

class AnimatedSVG:
    """解析一次动画定义，之后可输出任意时刻 t 的静态 SVG。"""

    def __init__(self, svg: str):
        self.svg = svg
        root = ET.fromstring(svg)
        elements = list(root.iter())
        index = {el: i for i, el in enumerate(elements)}
        parents = {child: parent for parent in elements for child in parent}
        ids = {el.get("id"): el for el in elements if el.get("id")}
        view_box = [float(n) for n in NUMBER_RE.findall(root.get("viewBox") or "")]
        self.view_size = (view_box[2], view_box[3]) if len(view_box) == 4 else (
            num(root.get("width"), 100), num(root.get("height"), 100))
        self.durations: list[float] = []

        self.smil: list[tuple[int, int]] = []  # (动画元素下标, 目标元素下标)
        for el in elements:
            if local(el.tag) not in SMIL_TAGS:
                continue
            href = (el.get("href") or el.get(XLINK_HREF) or "").lstrip("#")
            target = ids.get(href) if href else parents.get(el)
            if target is not None:
                self.smil.append((index[el], index[target]))
                begin, dur = smil_begin(el), clock(el.get("dur"))
                if begin is not None and dur:
                    self.durations.append(max(begin, 0) + dur)

        rules: list = []
        keyframes: dict = {}
        parse_css("\n".join("".join(el.itertext()) for el in elements if local(el.tag) == "style"),
                  rules, keyframes)
        rules.sort(key=lambda rule: rule[:2])
        self.css: list[CssTarget] = []
        self.boxes: dict[int, tuple] = {}
        for el in elements[1:]:
            declarations = [d for _, _, compounds, decls in rules if matches(compounds, el, parents) for d in decls]
            declarations += parse_declarations(el.get("style") or "")
            props = dict(declarations)
            if el.get("transform-origin"):
                props.setdefault("transform-origin", el.get("transform-origin"))
            animations = css_animations(declarations, keyframes)
            if animations or "transform" in props or "transform-origin" in props:
                self.css.append(CssTarget(index[el], props, animations))
                self.durations += [max(a.delay, 0) + a.duration for a in animations]
                if props.get("transform-box") == "fill-box":
                    self.boxes[index[el]] = element_bbox(el)

    @property
    def animation_count(self) -> int:
        return len(self.smil) + sum(len(target.animations) for target in self.css)

    @property
    def cycle(self) -> float:
        """完整展示所有动画的时长：最长的一段动画，上限 MAX_CYCLE 秒。"""
        return min(max(self.durations), MAX_CYCLE) if self.durations else 0.0

    def sample_times(self, count: int) -> list[float]:
        """给评委的采样时刻。窗口取各段动画时长的中位数，聚焦踩踏、车轮等主体的循环动作，
        而不是被几十秒一圈的背景漂移拉长；步长取窗口的 1/(count+1)，避免与整数周期对齐。"""
        if not self.durations:
            return [0.0] * count
        step = min(statistics.median(self.durations), MAX_CYCLE) / (count + 1)
        return [round(k * step, 3) for k in range(count)]

    def bake(self, t: float) -> str:
        root = ET.fromstring(self.svg)
        elements = list(root.iter())
        ids = {el.get("id"): el for el in elements if el.get("id")}
        for anim, target in self.smil:
            apply_smil(elements[anim], elements[target], t, ids)
        for target in self.css:
            el = elements[target.index]
            values = target.values_at(t, el)
            for prop, value in values.items():
                if prop != "transform":
                    set_style(el, prop, value)
            self._bake_transform(el, target, values.get("transform"))
        return ET.tostring(root, encoding="unicode")

    def _bake_transform(self, el, target: CssTarget, css_transform: str | None) -> None:
        """CSS transform 覆盖 transform 属性；transform-origin（含 fill-box）在这里折算进 transform，
        再把 transform-origin 置 0，因为 resvg 不支持 transform-box。"""
        box = self.boxes.get(target.index)
        bx, by, bw, bh = (box[0], box[1], box[2] - box[0], box[3] - box[1]) if box else (0, 0, *self.view_size)
        if css_transform is None:
            transform = el.get("transform", "")
        else:
            functions = parse_transform(css_transform)
            if functions is None:
                return
            parts = []
            for name, args in functions:
                if name == "translate":
                    x, y = (n * size / 100 if unit == "%" else n for (n, unit), size in zip(args, (bw, bh)))
                    parts.append(f"translate({fmt_num(x)} {fmt_num(y)})")
                else:
                    parts.append(f"{name}({' '.join(fmt_num(n) for n, _ in args)})")
            transform = " ".join(parts)
        if not transform:
            el.attrib.pop("transform", None)
            return
        ox, oy = self._origin(target.props.get("transform-origin"), bx, by, bw, bh)
        el.set("transform", f"translate({fmt_num(ox)} {fmt_num(oy)}) {transform} "
                            f"translate({fmt_num(-ox)} {fmt_num(-oy)})")
        set_style(el, "transform-origin", "0 0")

    @staticmethod
    def _origin(spec: str | None, bx: float, by: float, bw: float, bh: float) -> tuple[float, float]:
        """transform-origin 在参照框中的坐标；SVG 元素默认 0 0。"""
        keywords = {"left": "0%", "center": "50%", "right": "100%", "top": "0%", "bottom": "100%"}
        tokens = (spec or "0 0").split()[:2]
        if len(tokens) == 1:
            tokens.append("center")
        if tokens[0] in ("top", "bottom") or tokens[1] in ("left", "right"):
            tokens.reverse()
        x, y = (keywords.get(token, token) for token in tokens)
        return (bx + (num(x) * bw / 100 if x.endswith("%") else num(x)),
                by + (num(y) * bh / 100 if y.endswith("%") else num(y)))

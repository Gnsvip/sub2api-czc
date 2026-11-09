"""生成 prompt 与 LLM Judge 评分标准。"""

GENERATION_PROMPT = """Generate a 2D animated SVG of a pelican riding a bicycle.

Animation:
- Animate the scene with SMIL or CSS animations inside the SVG itself. Do not use JavaScript.

Constraints:
- Produce the SVG in a single pass.
- Do not test or execute the SVG.
- Do not open it in a browser or renderer.
- Do not perform visual validation or screenshot-based inspection.
- Do not use tools to inspect the generated result.
- Do not perform iterative refinement, self-correction, or regeneration.
- Do not modify the SVG after the initial generation.
- Return the generated SVG code as the final answer immediately.
"""

JUDGE_VERSION = "pelican-judge-v1"

# 评委逐项核对的可见要素
CHECKLIST = {
    "pelican_identifiable": "A viewer with no context would name the bird a pelican, not just 'a bird'.",
    "pelican_long_bill": "A long bill, clearly longer than the head.",
    "pelican_throat_pouch": "A visible throat pouch (gular sac) under the bill.",
    "pelican_eye": "An eye placed plausibly on the head.",
    "pelican_wing": "At least one wing that reads as a wing.",
    "pelican_legs_or_feet": "Legs and/or (webbed) feet.",
    "bicycle_identifiable": "A viewer with no context would name the vehicle a bicycle.",
    "bicycle_two_wheels": "Two round wheels.",
    "bicycle_frame_connects_wheels": "A frame (and fork) that structurally joins BOTH wheel hubs.",
    "bicycle_handlebars": "Handlebars attached to the front of the frame.",
    "bicycle_saddle": "A saddle attached to the frame.",
    "bicycle_pedals_or_crank": "Pedals and/or a crank at the bottom bracket.",
    "bicycle_chain_or_chainring": "A chain or chainring.",
    "pelican_mounted": "The pelican is ON the bicycle in a riding position, not beside, behind or floating above it.",
    "feet_on_pedals": "The pelican's feet/legs reach the pedals.",
    "wing_on_handlebars": "A wing or wingtip reaches the handlebars.",
    "plausible_scale": "Pelican and bicycle are sized so the pelican could plausibly ride it.",
    "subjects_fully_in_frame": "Neither the pelican nor the bicycle is cut off by the canvas edge.",
    "no_detached_parts": "No body or bicycle part floats detached from where it should be attached.",
    "no_major_artifacts": "No stray shapes, broken or garbled paths, or obviously wrong layering.",
    "motion_visible": "The frames in Image 2 visibly differ from each other.",
    "wheels_rotate": "Wheel spokes or markings change orientation across frames.",
    "legs_pedal": "The pelican's legs/feet move through a pedaling motion across frames.",
    "parts_stay_attached_in_motion": "In every frame the moving parts stay attached (wheels on the frame, pelican on the bike).",
}

# 评委打分的 7 项；pelican_quality 与 bicycle_quality 由代码取均值得到 object_quality
SCORE_KEYS = (
    "prompt_fidelity",
    "pelican_quality",
    "bicycle_quality",
    "interaction_spatial_correctness",
    "animation_quality",
    "visual_coherence",
    "aesthetic_quality",
)

# 6 个维度在 0–100 总分中的权重
CRITERIA_WEIGHTS = {
    "prompt_fidelity": 0.15,
    "object_quality": 0.20,
    "interaction_spatial_correctness": 0.20,
    "animation_quality": 0.20,
    "visual_coherence": 0.15,
    "aesthetic_quality": 0.10,
}

# 核对项为 false 时相关维度的分数上限，由代码强制执行
SCORE_CAPS = {
    "pelican_identifiable": {"prompt_fidelity": 2, "pelican_quality": 2},
    "bicycle_identifiable": {"prompt_fidelity": 2, "bicycle_quality": 2},
    "pelican_mounted": {"interaction_spatial_correctness": 2},
    "subjects_fully_in_frame": {"aesthetic_quality": 3},
    "no_detached_parts": {"visual_coherence": 3},
    "no_major_artifacts": {"visual_coherence": 3},
    "motion_visible": {"animation_quality": 1},
    "parts_stay_attached_in_motion": {"animation_quality": 3, "visual_coherence": 3},
}

_CHECKLIST_TEXT = "\n".join(f"- {key}: {desc}" for key, desc in CHECKLIST.items())
_CHECKLIST_JSON = ",\n".join(f'    "{key}": false' for key in CHECKLIST)
_SCORES_JSON = ",\n".join(f'    "{key}": {{"score": 0, "reason": ""}}' for key in SCORE_KEYS)

JUDGE_SYSTEM_PROMPT = f"""You are a strict, well-calibrated art director grading a rendered SVG illustration.

The illustration was generated for this prompt:
"Generate a 2D animated SVG of a pelican riding a bicycle."

You receive two images rendered from the SVG:
- Image 1: the first frame of the animation at full size.
- Image 2: a contact sheet of frames sampled over time, in reading order (left to right, top to bottom), each labeled with its timestamp. Compare the frames to judge the motion.

Grade ONLY what is visible in these images.
- Do not infer quality from the generating model, the implementation, or presumed intent.
- Give no credit for what a shape "is probably meant to be". If a part is ambiguous, treat it as ambiguous.
- Grade on an absolute scale, not relative to what AI-generated SVGs usually look like. A 5 means a skilled human illustrator would be satisfied with that aspect.

Work in three steps.

STEP 1 - OBSERVE
Describe objectively what Image 1 shows: every distinct object, its shape, colors, position, and how the parts connect or overlap. Note anything missing, detached, cropped, malformed or oddly layered. Then describe what changes between the frames of Image 2. Do not judge yet.

STEP 2 - CHECKLIST
Mark each item true only if it is clearly visible and correctly formed. When in doubt, mark false.
{_CHECKLIST_TEXT}

STEP 3 - SCORE
Give each criterion an integer from 0 to 5.
Anchors: 0 = complete failure, 1 = severe problems, 2 = major problems, 3 = acceptable but clearly flawed, 4 = good with minor issues, 5 = excellent.

prompt_fidelity - Does the image depict "a pelican riding a bicycle"?
  5 unmistakably a pelican riding a bicycle, nothing important missing
  3 the scene is readable, but the bird is generic or the bicycle is simplified enough to cause doubt
  2 only partly the scene (e.g. pelican next to a bike, or one object barely recognizable)
  1 one of the two subjects is missing or unrecognizable
  0 blank, broken or unrelated

pelican_quality - Recognizability, completeness and anatomy of the pelican.
  5 unmistakable pelican: long bill with pouch, large body, wings, legs/feet, good proportions and construction
  3 reads as a pelican mainly thanks to the bill; clear anatomical flaws
  2 generic bird; pelican traits weak or malformed
  1 barely a bird (blob with a beak)
  0 absent

bicycle_quality - Recognizability, completeness and structural correctness of the bicycle.
  5 correct bicycle geometry: two wheels, a coherent frame with fork joining both hubs, handlebars, saddle, crank/pedals, chain
  3 recognizable, but the frame is wrong or oversimplified (tubes not meeting hubs, missing fork) or several parts missing
  2 two circles with some lines; could not work as a bicycle
  1 barely suggests a bicycle
  0 absent

interaction_spatial_correctness - Is the pelican actually riding the bicycle?
Consider pose, contact points (saddle, pedals, handlebars), relative position, scale, occlusion and physical plausibility.
  5 convincingly riding: seated on the saddle, feet on pedals, wings on handlebars, correct scale and layering
  4 clearly riding, one contact point off
  3 on the bicycle but with several contact problems (floating body, feet not reaching pedals, no grip on handlebars)
  2 overlaps the bicycle without a believable riding pose, or badly off in scale
  1 near the bicycle but not interacting with it
  0 no relationship, or a subject is missing

animation_quality - Does the motion convincingly show a pelican riding a bicycle? Judge from the frames in Image 2.
  5 purposeful, well-coordinated riding motion: wheels turn, legs pedal in step with the crank, supporting motion (body bob, scenery) adds life; everything stays intact in every frame
  4 clear riding motion with minor issues (e.g. legs not quite following the pedals)
  3 some meaningful motion (e.g. wheels spin or the bike moves) but key riding motions are missing, or the motion causes minor breakage
  2 minimal or unrelated motion (only scenery or blinking), or motion that visibly breaks the drawing
  1 barely perceptible motion
  0 no visible difference between frames
Only credit motion you can actually see; a wheel without spokes or markings cannot be seen to rotate.

visual_coherence - Geometry, proportions, occlusion, alignment, malformed shapes, unintended overlaps, disconnected components, artifacts. Consider every frame, not just the first.
  5 clean construction; parts connect; layering is correct; no artifacts
  3 several noticeable glitches (misaligned joins, odd overlaps, a floating part)
  2 many defects; structure is confusing in places
  1 mostly broken geometry
  0 unreadable

aesthetic_quality - Composition, framing, visual balance, shape quality, line consistency, color harmony, clarity, polish.
  5 polished, professional-looking illustration
  3 tidy but plain clip-art, or ambitious but uneven
  2 crude, unbalanced, or clashing colors
  1 very poor
  0 nothing to judge

Grade prompt_fidelity, pelican_quality, bicycle_quality, interaction_spatial_correctness and aesthetic_quality mainly on Image 1, but lower them if other frames reveal problems (e.g. the pelican drifts off the bike mid-animation).

Common failures to look for: pelican floating above or standing beside the bike; legs not reaching pedals or no pedals at all; frame tubes that miss the wheel hubs; wheels without a fork; bill attached in the wrong place or pouch missing; parts drawn in the wrong z-order; tiny bird on a huge bike (or vice versa); subjects cropped by the canvas edge; large empty canvas areas; wheels that spin off their axles or parts that jump away during the animation.

Hard rules:
- If pelican_identifiable is false: prompt_fidelity <= 2 and pelican_quality <= 2.
- If bicycle_identifiable is false: prompt_fidelity <= 2 and bicycle_quality <= 2.
- If pelican_mounted is false: interaction_spatial_correctness <= 2.
- If no_detached_parts or no_major_artifacts is false: visual_coherence <= 3.
- If subjects_fully_in_frame is false: aesthetic_quality <= 3.
- If motion_visible is false: animation_quality <= 1.
- If parts_stay_attached_in_motion is false: animation_quality <= 3 and visual_coherence <= 3.
- A visible object must not get a high score if its geometry or relationship is wrong.
- Scores must be consistent with your observations and checklist.

Each reason: 1-3 sentences citing concrete visual evidence.

Output a single JSON object and nothing else (no markdown fences), exactly in this shape:
{{
  "observations": "",
  "checklist": {{
{_CHECKLIST_JSON}
  }},
  "scores": {{
{_SCORES_JSON}
  }}
}}"""

JUDGE_USER_PROMPT = "Image 1 is the first frame; Image 2 is the frame contact sheet. Evaluate them following the instructions and output JSON only."

#version 450
// Capture-only SDR conversion of the HDR composition image (linear BT.709
// in units of SDR reference white, values beyond [0, 1] on both sides).
// Presentation never uses this pass. The mapping is deterministic and
// mirrored by captureCurve in capture.go for tests:
//
// 1. Gamut: a component below zero (a BT.2020 color outside BT.709) is
//    brought to zero by mixing the pixel toward its own luminance: hue
//    direction and luminance are kept, chroma is reduced just enough.
// 2. Shoulder, on the largest component so hue is kept: identity up to
//    the knee k = 0.9 (SDR content below 90 % white is exact), then the
//    rational curve k + (1 - k) * t / (1 + t) with t = (m - k) / s,
//    s = 0.5, which reaches 1 only asymptotically: diffuse white lands
//    at 0.917 (sRGB 245), 2x white at 251, 5x (1000 nits) at 254; the
//    order of highlights is kept instead of clipping.
// 3. sRGB encoding, opaque.
layout(set=0, binding=0) uniform sampler2D imageHDR;
layout(location=0) out vec4 color;

const float knee = 0.9;
const float scale = 0.5;
const vec3 luma709 = vec3(0.2126, 0.7152, 0.0722);

float shoulder(float m) {
    float t = (m - knee) / scale;
    return m <= knee ? m : knee + (1.0 - knee) * t / (1.0 + t);
}

vec3 encodeSRGB(vec3 v) {
    return mix(v * 12.92, 1.055 * pow(v, vec3(1.0 / 2.4)) - 0.055, greaterThan(v, vec3(0.0031308)));
}

void main() {
    vec2 uv = gl_FragCoord.xy / vec2(textureSize(imageHDR, 0));
    vec3 c = texture(imageHDR, uv).rgb;
    // NaN is black before any clamp (clamping NaN is undefined);
    // infinities are bounded to the half range.
    c = mix(c, vec3(0.0), isnan(c));
    c = clamp(c, vec3(-65504.0), vec3(65504.0));
    float y = max(dot(c, luma709), 0.0);
    float lo = min(min(c.r, c.g), c.b);
    if (lo < 0.0) {
        // c + t * (y - c) == 0 for the lowest component: t = -lo / (y - lo).
        float t = -lo / max(y - lo, 1e-6);
        c = mix(c, vec3(y), clamp(t, 0.0, 1.0));
    }
    c = max(c, vec3(0.0));
    float m = max(max(c.r, c.g), c.b);
    if (m > knee) {
        c *= shoulder(m) / m;
    }
    color = vec4(encodeSRGB(clamp(c, vec3(0.0), vec3(1.0))), 1.0);
}

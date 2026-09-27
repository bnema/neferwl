#version 450
// Draws a solid fill, a client dmabuf (sampled image) or a client wl_shm
// buffer (B8G8R8A8 words in a storage buffer). Client pixels are
// premultiplied, as Wayland buffers are: the pipeline blends ONE,
// ONE_MINUS_SRC_ALPHA. Scaled draws filter linearly; 1:1 draws read the
// texel under the fragment, pixel exact.

layout(push_constant) uniform Draw {
    ivec4 rect;
    vec4 map;
    vec4 color;
    uvec4 buf;
    uvec4 misc;
} d;

layout(set = 0, binding = 0) uniform sampler2D tex;
layout(std430, set = 0, binding = 1) readonly buffer Pixels { uint pixels[]; };
layout(set = 0, binding = 2) uniform sampler2D chromaTex;

layout(location = 0) out vec4 color;

const uint modeSolid = 0u;
const uint modeImage = 1u;
const uint flagOpaque = 1u;
const uint flagExact = 2u;
const uint flagPQ = 4u;
const uint flagExtendedLinear = 8u;
const uint flagYUV = 16u;
const uint flagP010 = 32u;

// The HDR composition target holds linear BT.709 in SDR-white units.
vec3 decodeSRGB(vec3 v) {
    return mix(v / 12.92, pow((v + 0.055) / 1.055, vec3(2.4)), greaterThan(v, vec3(0.04045)));
}
vec3 pqToLinear709(vec3 v, float whiteNits) {
    vec3 p = pow(clamp(v, 0.0, 1.0), vec3(32.0 / 2523.0));
    vec3 nits = pow(max(p - vec3(3424.0 / 4096.0), vec3(0.0)) /
        max(vec3(2413.0 / 128.0) - p * (2392.0 / 128.0), vec3(0.00001)), vec3(16384.0 / 2610.0)) * 10000.0;
    return vec3(
        dot(nits, vec3(1.660491, -0.587641, -0.072850)),
        dot(nits, vec3(-0.124550, 1.132900, -0.008349)),
        dot(nits, vec3(-0.018151, -0.100579, 1.118730))
    ) / whiteNits;
}

// H.273 Y'CbCr 4:2:0 reconstruction in electrical (non-linear) light.
// Type 0 places chroma at x=even luma and halfway between luma rows.
// vec2 coordinates are normalized to the chroma image for linear filtering.
vec4 sampleYUV(vec2 src) {
    ivec2 size = textureSize(tex, 0);
    vec2 lumaUV = src / vec2(size);
    float y = texture(tex, lumaUV).r;
    vec2 chromaUV = vec2((src.x + 0.5) / float(size.x), src.y / float(size.y));
    vec2 cbcr = texture(chromaTex, chromaUV).rg;
    bool tenBit = (d.misc.y & flagP010) != 0u;
    bool limited = ((d.buf.w >> 8) & 255u) == 2u;
    float maxCode = tenBit ? 1023.0 : 255.0;
    // Packed 10-bit plane views normalize directly to the 0..1023 code range.
    float yOff = limited ? (tenBit ? 64.0 : 16.0) / maxCode : 0.0;
    float yScale = limited ? maxCode / (tenBit ? 876.0 : 219.0) : 1.0;
    float cScale = limited ? maxCode / (tenBit ? 896.0 : 224.0) : 1.0;
    float yy = (y - yOff) * yScale;
    vec2 cc = (cbcr - vec2(tenBit ? 512.0/1023.0 : 128.0/255.0)) * cScale;
    uint coeff = d.buf.w & 255u;
    float kr = coeff == 4u ? 0.299 : coeff == 6u ? 0.2627 : 0.2126;
    float kb = coeff == 4u ? 0.114 : coeff == 6u ? 0.0593 : 0.0722;
    vec3 rgb = vec3(yy + 2.0*(1.0-kr)*cc.y,
        yy - 2.0*kb*(1.0-kb)/(1.0-kr-kb)*cc.x - 2.0*kr*(1.0-kr)/(1.0-kr-kb)*cc.y,
        yy + 2.0*(1.0-kb)*cc.x);
    return vec4(rgb, 1.0);
}

// texel reads buffer pixel p, clamped to the buffer (clamp to edge).
vec4 texel(ivec2 p) {
    p = clamp(p, ivec2(0), ivec2(d.buf.yz) - 1);
    // Little-endian B,G,R,A bytes: unpack yields (B, G, R, A).
    return unpackUnorm4x8(pixels[d.buf.x + uint(p.y) * d.buf.y + uint(p.x)]).zyxw;
}

void main() {
    vec2 src = d.map.xy + gl_FragCoord.xy * d.map.zw;
    bool exact = (d.misc.y & flagExact) != 0u;
    vec4 c;
    if (d.misc.x == modeSolid) {
        c = vec4(decodeSRGB(d.color.rgb), d.color.a);
    } else if (d.misc.x == modeImage && (d.misc.y & flagYUV) != 0u) {
        c = sampleYUV(src);
    } else if (d.misc.x == modeImage) {
        ivec2 size = textureSize(tex, 0);
        c = exact ? texelFetch(tex, clamp(ivec2(floor(src)), ivec2(0), size - 1), 0) : texture(tex, src / vec2(size));
    } else if (exact) {
        c = texel(ivec2(floor(src)));
    } else {
        // Bilinear: four texel reads around the sample point.
        vec2 p = src - 0.5;
        ivec2 i = ivec2(floor(p));
        vec2 f = p - vec2(i);
        c = mix(mix(texel(i), texel(i + ivec2(1, 0)), f.x), mix(texel(i + ivec2(0, 1)), texel(i + ivec2(1, 1)), f.x), f.y);
    }
    if ((d.misc.y & flagOpaque) != 0u) {
        c.a = 1.0;
    }
    if (c.a > 0.0 && d.misc.x != modeSolid) {
        // ext_linear has the protocol's default 80 cd/m² reference white.
        // Keep negative and above-white values in fp16 until the HDR pass.
        vec3 linearRGB = (d.misc.y & flagPQ) != 0u ? pqToLinear709(c.rgb / c.a, d.color.x) :
            (d.misc.y & flagExtendedLinear) != 0u ? c.rgb / c.a * (80.0 / d.color.x) : decodeSRGB(c.rgb / c.a);
        c.rgb = linearRGB * c.a;
    }
    color = c;
}

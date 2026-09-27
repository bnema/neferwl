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

layout(location = 0) out vec4 color;

const uint modeSolid = 0u;
const uint modeImage = 1u;
const uint flagOpaque = 1u;
const uint flagExact = 2u;
const uint flagPQ = 4u;

// PQ values are absolute nits. BT.2020 is converted in linear light to
// BT.709, then clipped to the 8-bit sRGB intermediate. Highlights above
// SDR reference white are lost (no tone mapping).
vec3 pqToSRGB(vec3 v, float whiteNits) {
    vec3 p = pow(clamp(v, 0.0, 1.0), vec3(32.0 / 2523.0));
    vec3 nits = pow(max(p - vec3(3424.0 / 4096.0), vec3(0.0)) /
        max(vec3(2413.0 / 128.0) - p * (2392.0 / 128.0), vec3(0.00001)), vec3(16384.0 / 2610.0)) * 10000.0;
    vec3 rgb = vec3(
        dot(nits, vec3(1.660491, -0.587641, -0.072850)),
        dot(nits, vec3(-0.124550, 1.132900, -0.008349)),
        dot(nits, vec3(-0.018151, -0.100579, 1.118730))
    );
    rgb = clamp(rgb / whiteNits, 0.0, 1.0);
    return mix(rgb * 12.92, 1.055 * pow(rgb, vec3(1.0 / 2.4)) - 0.055, greaterThan(rgb, vec3(0.0031308)));
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
        c = d.color;
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
    if ((d.misc.y & flagPQ) != 0u && c.a > 0.0) {
        c.rgb = pqToSRGB(c.rgb / c.a, d.color.x) * c.a;
    }
    color = c;
}

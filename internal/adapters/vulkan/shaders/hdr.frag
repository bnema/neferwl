#version 450
layout(set=0, binding=0) uniform sampler2D imageSDR;
layout(push_constant) uniform Brightness { float nits; } pc;
layout(location=0) out vec4 color;

vec3 decodeSRGB(vec3 c) {
    return mix(c / 12.92, pow((c + 0.055) / 1.055, vec3(2.4)), greaterThan(c, vec3(0.04045)));
}
vec3 encodePQ(vec3 nits) {
    vec3 x = pow(clamp(nits / 10000.0, vec3(0), vec3(1)), vec3(2610.0 / 16384.0));
    return pow((3424.0 / 4096.0 + 2413.0 / 128.0 * x) / (1.0 + 2392.0 / 128.0 * x), vec3(2523.0 / 32.0));
}
void main() {
    vec2 uv = gl_FragCoord.xy / vec2(textureSize(imageSDR, 0));
    vec3 rgb = decodeSRGB(texture(imageSDR, uv).rgb);
    vec3 bt2020 = mat3(
        0.627404, 0.069097, 0.016391,
        0.329283, 0.919540, 0.088013,
        0.043313, 0.011362, 0.895595) * rgb;
    color = vec4(encodePQ(bt2020 * pc.nits), 1.0);
}

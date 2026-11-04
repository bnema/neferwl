#version 450
layout(set=0,binding=0) uniform sampler2D backgroundImage;
layout(std430,set=0,binding=1) readonly buffer Cursor { uint pixels[]; } cursor;
layout(push_constant) uniform Params { ivec4 rect; int mode; float nits; } pc;
layout(location=0) out vec4 color;
vec3 linearize(vec3 v) { return mix(v/12.92,pow((v+0.055)/1.055,vec3(2.4)),greaterThan(v,vec3(0.04045))); }
vec3 encode(vec3 v) { return mix(v*12.92,1.055*pow(v,vec3(1.0/2.4))-0.055,greaterThan(v,vec3(0.0031308))); }
vec3 pq(vec3 n) { vec3 x=pow(clamp(n/10000.0,0.0,1.0),vec3(2610.0/16384.0)); return pow((3424.0/4096.0+2413.0/128.0*x)/(1.0+2392.0/128.0*x),vec3(2523.0/32.0)); }
void main() {
    vec3 c=texelFetch(backgroundImage,ivec2(gl_FragCoord.xy),0).rgb;
    if(pc.mode==0) c=linearize(c);
    ivec2 p=ivec2(gl_FragCoord.xy)-pc.rect.xy;
    if(all(greaterThanEqual(p,ivec2(0))) && all(lessThan(p,pc.rect.zw))) {
        vec4 s=unpackUnorm4x8(cursor.pixels[p.y*pc.rect.z+p.x]).bgra;
        if(s.a>0.0) c=linearize(clamp(s.rgb/s.a,0.0,1.0))*s.a+c*(1.0-s.a);
    }
    if(pc.mode==2) {
        vec3 bt2020=mat3(0.627404,0.069097,0.016391,0.329283,0.919540,0.088013,0.043313,0.011362,0.895595)*c;
        color=vec4(pq(bt2020*pc.nits),1.0); return;
    }
    if(pc.mode==1) {
        c=mix(c,vec3(0),isnan(c)); c=clamp(c,-65504.0,65504.0);
        float y=max(dot(c,vec3(0.2126,0.7152,0.0722)),0.0);
        float lo=min(min(c.r,c.g),c.b);
        if(lo<0.0) c=mix(c,vec3(y),clamp(-lo/max(y-lo,1e-6),0.0,1.0));
        c=max(c,0.0); float m=max(max(c.r,c.g),c.b);
        if(m>0.9) { float t=(m-0.9)/0.5; c*=(0.9+0.1*t/(1.0+t))/m; }
    }
    color=vec4(encode(clamp(c,0.0,1.0)),1.0);
}

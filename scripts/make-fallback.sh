#!/bin/sh
# Converts an image or a video into a fallback clip the relay can stream
# without re-encoding (H.264 + AAC, keyframe every 2s, loops cleanly).
#
# Usage: make-fallback <input image|video> <output.mp4> [WIDTHxHEIGHT] [FPS]
#   Defaults: 1920x1080, 30 fps. Env: DURATION (image clips, default 10s),
#   VIDEO_BITRATE (default 2500k).
set -eu

if [ $# -lt 2 ]; then
    echo "usage: make-fallback <input image|video> <output.mp4> [WIDTHxHEIGHT] [FPS]" >&2
    exit 2
fi

in=$1
out=$2
size=${3:-1920x1080}
fps=${4:-30}
w=${size%x*}
h=${size#*x}
dur=${DURATION:-10}
vbr=${VIDEO_BITRATE:-2500k}
gop=$((fps * 2))

vf="scale=${w}:${h}:force_original_aspect_ratio=decrease,pad=${w}:${h}:(ow-iw)/2:(oh-ih)/2:color=black,fps=${fps},format=yuv420p"
venc="-c:v libx264 -preset medium -profile:v high -g ${gop} -keyint_min ${gop} -sc_threshold 0 -bf 0 -b:v ${vbr} -maxrate ${vbr} -bufsize ${vbr}"
aenc="-c:a aac -b:a 128k -ar 48000 -ac 2"
silence="-f lavfi -i anullsrc=r=48000:cl=stereo"

case "$(printf '%s' "$in" | tr 'A-Z' 'a-z')" in
*.png | *.jpg | *.jpeg | *.webp | *.bmp)
    echo "image -> ${dur}s clip, ${w}x${h}@${fps}"
    # shellcheck disable=SC2086
    ffmpeg -hide_banner -loglevel warning -y -loop 1 -framerate "$fps" -i "$in" $silence \
        -t "$dur" -map 0:v -map 1:a -vf "$vf" -tune stillimage $venc $aenc -movflags +faststart "$out"
    ;;
*)
    if ffprobe -v error -select_streams a:0 -show_entries stream=index -of csv=p=0 "$in" | grep -q .; then
        echo "video with audio -> ${w}x${h}@${fps}"
        # shellcheck disable=SC2086
        ffmpeg -hide_banner -loglevel warning -y -i "$in" \
            -map 0:v:0 -map 0:a:0 -vf "$vf" $venc $aenc -movflags +faststart "$out"
    else
        echo "video without audio -> adding silence, ${w}x${h}@${fps}"
        # shellcheck disable=SC2086
        ffmpeg -hide_banner -loglevel warning -y -i "$in" $silence \
            -map 0:v:0 -map 1:a -shortest -vf "$vf" $venc $aenc -movflags +faststart "$out"
    fi
    ;;
esac

echo "written: $out"

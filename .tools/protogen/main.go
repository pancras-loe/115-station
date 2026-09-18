// protogen：无 protoc 的纯 Go proto 编译驱动。
// 用 bufbuild/protocompile 解析 clouddrive.proto（含 google 标准导入），
// 组装 CodeGeneratorRequest 后喂给 protoc-gen-go，把生成的 .pb.go 写到输出目录。
//
// 用法: go run . <proto目录> <输出目录>
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	pluginpb "google.golang.org/protobuf/types/pluginpb"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: protogen <protoDir> <outDir>")
		os.Exit(2)
	}
	protoDir, outDir := os.Args[1], os.Args[2]

	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			ImportPaths: []string{protoDir},
		}),
	}
	fds, err := comp.Compile(context.Background(), "clouddrive.proto")
	if err != nil {
		panic(err)
	}

	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"clouddrive.proto"},
		Parameter:      proto.String("Mclouddrive.proto=strmhub/internal/cd2/pb"),
	}
	// 递归收集全部 import 依赖（google 标准类型由 WithStandardImports 提供），
	// protoc-gen-go 要求请求里包含完整依赖闭包
	seen := map[string]bool{}
	var collect func(fd protoreflect.FileDescriptor)
	collect = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		// 后序：依赖先于使用方入列（protoc-gen-go 按序注册解析 import）
		for i := 0; i < fd.Imports().Len(); i++ {
			collect(fd.Imports().Get(i))
		}
		req.ProtoFile = append(req.ProtoFile, protodesc.ToFileDescriptorProto(fd))
	}
	for _, fd := range fds {
		collect(fd)
	}
	body, err := proto.Marshal(req)
	if err != nil {
		panic(err)
	}

	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		out, err := exec.Command("go", "env", "GOPATH").Output()
		if err != nil {
			panic(err)
		}
		gopath = strings.TrimSpace(string(out))
	}
	genBin := filepath.Join(gopath, "bin", "protoc-gen-go")
	if _, err := os.Stat(genBin); err != nil {
		genBin += ".exe"
	}
	cmd := exec.Command(genBin)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		panic(err)
	}

	res := &pluginpb.CodeGeneratorResponse{}
	if err := proto.Unmarshal(out, res); err != nil {
		panic(err)
	}
	if e := res.GetError(); e != "" {
		panic(e)
	}
	for _, f := range res.File {
		dst := filepath.Join(outDir, filepath.Base(f.GetName()))
		if err := os.WriteFile(dst, []byte(f.GetContent()), 0o644); err != nil {
			panic(err)
		}
		fmt.Println("wrote", dst)
	}
}

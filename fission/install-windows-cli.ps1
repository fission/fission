go install
if (Test-Path $Env:GOPATH\bin\fission-test.exe -PathType Leaf) {
    rm $Env:GOPATH\bin\fission-test.exe
}
mv $Env:GOPATH\bin\fission.exe $Env:GOPATH\bin\fission-test.exe
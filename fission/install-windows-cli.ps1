go install
rm $Env:GOPATH\bin\fission-test.exe
mv $Env:GOPATH\bin\fission.exe $Env:GOPATH\bin\fission-test.exe
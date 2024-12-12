package virtctl

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kubevirt.io/client-go/kubecli"

	"kubevirt.io/kubevirt/pkg/libvmi"
	libvmici "kubevirt.io/kubevirt/pkg/libvmi/cloudinit"
	"kubevirt.io/kubevirt/tests/clientcmd"
	"kubevirt.io/kubevirt/tests/console"
	"kubevirt.io/kubevirt/tests/decorators"
	"kubevirt.io/kubevirt/tests/framework/kubevirt"
	"kubevirt.io/kubevirt/tests/libssh"
	"kubevirt.io/kubevirt/tests/libvmifact"
	"kubevirt.io/kubevirt/tests/libwait"
	"kubevirt.io/kubevirt/tests/testsuite"
)

var _ = Describe("[sig-compute][virtctl]SCP", decorators.SigCompute, func() {
	var pub ssh.PublicKey
	var keyFile string
	var virtClient kubecli.KubevirtClient

	copyNative := func(src, dst string, recursive bool) {
		args := []string{
			"scp",
			"--local-ssh=false",
			"--namespace", testsuite.GetTestNamespace(nil),
			"--username", "root",
			"--identity-file", keyFile,
			"--known-hosts=",
		}
		if recursive {
			args = append(args, "--recursive")
		}
		args = append(args, src, dst)
		Expect(clientcmd.NewRepeatableVirtctlCommand(args...)()).To(Succeed())
	}

	copyLocal := func(appendLocalSSH bool) func(src, dst string, recursive bool) {
		return func(src, dst string, recursive bool) {
			args := []string{
				"scp",
				"--namespace", testsuite.GetTestNamespace(nil),
				"--username", "root",
				"--identity-file", keyFile,
				"-t", "-o StrictHostKeyChecking=no",
				"-t", "-o UserKnownHostsFile=/dev/null",
			}
			if appendLocalSSH {
				args = append(args, "--local-ssh=true")
			}
			if recursive {
				args = append(args, "--recursive")
			}
			args = append(args, src, dst)

			// The virtctl binary needs to run here because of the way local SCP client wrapping works.
			// Running the command through NewRepeatableVirtctlCommand does not suffice.
			cmdString, cmd, err := clientcmd.CreateCommandWithNS(testsuite.GetTestNamespace(nil), "virtctl", args...)
			fmt.Println("cmdString", cmdString)
			Expect(err).ToNot(HaveOccurred())
			out, err := cmd.CombinedOutput()
			fmt.Println("out,err", string(out), err)
			Expect(err).ToNot(HaveOccurred())
			Expect(out).ToNot(BeEmpty())
		}
	}

	BeforeEach(func() {
		virtClient = kubevirt.Client()
		// Disable SSH_AGENT to not influence test results
		Expect(os.Setenv("SSH_AUTH_SOCK", "/dev/null")).To(Succeed())
		keyFile = filepath.Join(GinkgoT().TempDir(), "id_rsa")
		var err error
		var priv *ecdsa.PrivateKey
		priv, pub, err = libssh.NewKeyPair()
		Expect(err).ToNot(HaveOccurred())
		Expect(libssh.DumpPrivateKey(priv, keyFile)).To(Succeed())
	})

	DescribeTable("should copy a local file back and forth", func(copyFn func(string, string, bool)) {
		By("injecting a SSH public key into a VMI")
		vmi := libvmifact.NewFedora(
			libvmi.WithCloudInitNoCloud(libvmici.WithNoCloudUserData(libssh.RenderUserDataWithKey(pub))),
		)
		vmi, err := virtClient.VirtualMachineInstance(testsuite.GetTestNamespace(nil)).Create(context.Background(), vmi, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())

		vmi = libwait.WaitUntilVMIReady(vmi, console.LoginToFedora)

		By("copying a file to the VMI")
		copyFn(keyFile, vmi.Name+":"+"./keyfile", false)

		By("copying the file back")
		copyBackFile := filepath.Join(GinkgoT().TempDir(), "remote_id_rsa")
		copyFn(vmi.Name+":"+"./keyfile", copyBackFile, false)

		By("comparing the two files")
		compareFile(keyFile, copyBackFile)
	},
		Entry("using the native scp method", decorators.NativeSsh, copyNative),
		Entry("using the local scp method with --local-ssh flag", decorators.NativeSsh, copyLocal(true)),
		Entry("using the local scp method without --local-ssh flag", decorators.ExcludeNativeSsh, copyLocal(false)),
	)

	DescribeTable("should copy a local directory back and forth", func(copyFn func(string, string, bool)) {
		By("injecting a SSH public key into a VMI")
		vmi := libvmifact.NewFedora(
			libvmi.WithCloudInitNoCloud(libvmici.WithNoCloudUserData(libssh.RenderUserDataWithKey(pub))),
		)
		vmi, err := virtClient.VirtualMachineInstance(testsuite.GetTestNamespace(nil)).Create(context.Background(), vmi, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())

		vmi = libwait.WaitUntilVMIReady(vmi, console.LoginToFedora)

		By("creating a few random files")
		copyFromDir := filepath.Join(GinkgoT().TempDir(), "sourcedir")
		copyToDir := filepath.Join(GinkgoT().TempDir(), "targetdir")

		Expect(os.Mkdir(copyFromDir, 0777)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(copyFromDir, "file1"), []byte("test"), 0777)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(copyFromDir, "file2"), []byte("test1"), 0777)).To(Succeed())

		By("copying a file to the VMI")
		copyFn(copyFromDir, vmi.Name+":"+"./sourcedir", true)

		By("copying the file back")
		copyFn(vmi.Name+":"+"./sourcedir", copyToDir, true)

		By("comparing the two directories")
		compareFile(filepath.Join(copyFromDir, "file1"), filepath.Join(copyToDir, "file1"))
		compareFile(filepath.Join(copyFromDir, "file2"), filepath.Join(copyToDir, "file2"))
	},
		Entry("using the native scp method", decorators.NativeSsh, copyNative),
		Entry("using the local scp method with --local-ssh flag", decorators.NativeSsh, copyLocal(true)),
		Entry("using the local scp method without --local-ssh flag", decorators.ExcludeNativeSsh, copyLocal(false)),
	)

	It("local-ssh flag should be unavailable in virtctl", decorators.ExcludeNativeSsh, func() {
		// The built virtctl binary should be tested here, therefore clientcmd.CreateCommandWithNS needs to be used.
		// Running the command through NewRepeatableVirtctlCommand would test the test binary instead.
		_, cmd, err := clientcmd.CreateCommandWithNS(testsuite.NamespaceTestDefault, "virtctl", "scp", "--local-ssh=false")
		Expect(err).ToNot(HaveOccurred())
		out, err := cmd.CombinedOutput()
		Expect(err).To(HaveOccurred(), "out[%s]", string(out))
		Expect(string(out)).To(Equal("unknown flag: --local-ssh\n"))
	})
})

func compareFile(file1 string, file2 string) {
	expected, err := os.ReadFile(file1)
	Expect(err).ToNot(HaveOccurred())
	actual, err := os.ReadFile(file2)
	Expect(err).ToNot(HaveOccurred())
	Expect(string(actual)).To(Equal(string(expected)))
}
